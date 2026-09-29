package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// deleteRequestBudget bounds the time one Delete call spends on delete
// steps. A subtree that needs more time returns a running job, and the
// resume loop finishes it.
const deleteRequestBudget = 2 * time.Second

// DeleteResult reports one subtree delete job.
type DeleteResult struct {
	// JobID identifies the job. DeleteStatus reads it.
	JobID uuid.UUID
	// Deleted counts the nodes the job deleted so far, the root included
	// once the root is gone.
	Deleted int
	// Moved counts the direct children of the root that the job moved to
	// the root's parent so far.
	Moved int
	// State is running until the job deletes the root, then finished.
	State node.SubtreeDeleteState
}

// Delete removes the node. Each direct child of the node moves to the node's
// own parent when node.LivesUnder places the child's type under that
// parent's type. Every other child is deleted with its descendants. The
// delete stores a job record in FoundationDB and runs bounded steps of the
// job for at most deleteRequestBudget. Each deleted node writes one
// node.delete ledger event and each moved node one node.update ledger event,
// in the transaction that deletes or moves it. A job that is still running
// when the budget ends, or when the caller stops, is finished by
// ResumeSubtreeDeletes.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) (DeleteResult, error) {
	ctx, span := telemetry.StartSpan(ctx, "service.node.delete",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", nodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", nodeID.String()), slog.String("actor_id", actorID.String()))
	none := DeleteResult{JobID: uuid.Nil, Deleted: 0, Moved: 0, State: node.SubtreeDeleteRunning}

	existing, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("read node %s to delete it: %w", nodeID, err)
	}
	if existing == nil {
		return none, domain.ErrNotFound
	}
	if err := audit.StageStateChange(ctx, audit.VerbNodeDelete, audit.Entity{
		Type: "node", NodeType: existing.NodeType, ID: nodeID, Identifier: "", Name: existing.Name,
	}); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("stage the audit row for deleting %s: %w", nodeID, err)
	}
	template, _ := auditintent.Pending(ctx)
	return s.startSubtreeDelete(ctx, existing.OrgID, nodeID, actorID, template, deleteRequestBudget)
}

// DeleteSubtree removes the node the way Delete does and runs the job until
// it finishes. template is the ledger event of the root delete. The step that
// deletes the root writes it unchanged, and every other deleted or moved node
// writes a copy with its own entity. An empty template writes no ledger
// event.
func (s *NodeService) DeleteSubtree(ctx context.Context, orgID, nodeID, actorID uuid.UUID, template json.RawMessage) (DeleteResult, error) {
	return s.startSubtreeDelete(ctx, orgID, nodeID, actorID, template, 0)
}

// startSubtreeDelete stores a new job record for nodeID and runs its steps
// within budget. A zero budget runs until the job finishes.
func (s *NodeService) startSubtreeDelete(
	ctx context.Context, orgID, nodeID, actorID uuid.UUID, template json.RawMessage, budget time.Duration,
) (DeleteResult, error) {
	none := DeleteResult{JobID: uuid.Nil, Deleted: 0, Moved: 0, State: node.SubtreeDeleteRunning}
	jobID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("create the delete job id for node %s: %w", nodeID, err)
	}
	job := &node.SubtreeDeleteJob{
		ID: jobID, OrgID: orgID, RootID: nodeID, ActorID: actorID, AuditTemplate: template,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := s.deleter.StartSubtreeDelete(ctx, job); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("start the delete of node %s: %w", nodeID, err)
	}
	result, err := s.runSubtreeDelete(ctx, jobID, budget)
	if err != nil {
		return none, err
	}
	telemetry.L(ctx).InfoContext(ctx, "node.deleted", slog.String("node_id", nodeID.String()), slog.Int("deleted", result.Deleted),
		slog.Int("moved", result.Moved), slog.String("job_id", jobID.String()), slog.String("state", string(result.State)))
	return result, nil
}

// runSubtreeDelete runs steps of one job until the job reports Done or the
// budget ends. A zero budget runs until Done.
func (s *NodeService) runSubtreeDelete(ctx context.Context, jobID uuid.UUID, budget time.Duration) (DeleteResult, error) {
	started := clock.Now()
	for {
		progress, err := s.deleter.DeleteSubtreeStep(ctx, jobID, subtreeChangeEvent)
		if err != nil {
			slog.ErrorContext(ctx, "node.subtree_delete.step_failed", slog.String("err", err.Error()), slog.String("job_id", jobID.String()))
			return DeleteResult{JobID: jobID, Deleted: 0, Moved: 0, State: node.SubtreeDeleteRunning},
				fmt.Errorf("run a step of delete job %s: %w", jobID, err)
		}
		state := node.SubtreeDeleteRunning
		if progress.Done {
			state = node.SubtreeDeleteFinished
		}
		result := DeleteResult{JobID: jobID, Deleted: progress.Deleted, Moved: progress.Moved, State: state}
		if progress.Done || (budget > 0 && clock.Since(started) >= budget) {
			return result, nil
		}
	}
}

// subtreeChangeEvent builds the ledger event of one deleted or moved node
// from the staged event of the deleted root: node.delete for a deleted node,
// and node.update with the parent_id change for a moved node.
func subtreeChangeEvent(template json.RawMessage, change node.SubtreeChange) (json.RawMessage, error) {
	entity := audit.Entity{Type: "node", NodeType: change.NodeType, ID: change.ID, Identifier: "", Name: change.Name}
	var payload json.RawMessage
	var err error
	if change.Moved() {
		payload, err = audit.DescendantMoveEvent(template, entity, change.MovedFrom, change.MovedTo)
	} else {
		payload, err = audit.DescendantDeleteEvent(template, entity)
	}
	if err != nil {
		slog.Error("node.subtree_delete.event_failed", slog.String("err", err.Error()), slog.String("node_id", change.ID.String()))
		return nil, fmt.Errorf("build the ledger event of node %s: %w", change.ID, err)
	}
	return payload, nil
}
