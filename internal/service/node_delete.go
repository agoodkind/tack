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
	// State is running until the job deletes the root, then finished.
	State node.SubtreeDeleteState
}

// Delete removes the node and every hierarchy descendant of the node.
// NodeType metadata decides which nodes are descendants. The delete stores a
// job record in FoundationDB and runs bounded steps of the job for at most
// deleteRequestBudget. Each deleted node writes one node.delete ledger event
// in the transaction that deletes it. A job that is still running when the
// budget ends, or when the caller stops, is finished by
// ResumeSubtreeDeletes.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) (DeleteResult, error) {
	ctx, span := telemetry.StartSpan(ctx, "service.node.delete",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", nodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", nodeID.String()), slog.String("actor_id", actorID.String()))
	none := DeleteResult{JobID: uuid.Nil, Deleted: 0, State: node.SubtreeDeleteRunning}

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
	jobID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("create the delete job id for node %s: %w", nodeID, err)
	}
	job := &node.SubtreeDeleteJob{
		ID: jobID, OrgID: existing.OrgID, RootID: nodeID, AuditTemplate: template,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := s.deleter.StartSubtreeDelete(ctx, job); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return none, fmt.Errorf("start the delete of node %s: %w", nodeID, err)
	}
	result, err := s.runSubtreeDelete(ctx, jobID, deleteRequestBudget)
	if err != nil {
		return none, err
	}
	telemetry.L(ctx).InfoContext(ctx, "node.deleted", slog.Int("deleted", result.Deleted),
		slog.String("job_id", jobID.String()), slog.String("state", string(result.State)))
	return result, nil
}

// runSubtreeDelete runs steps of one job until the job reports Done or the
// budget ends. A zero budget runs until Done.
func (s *NodeService) runSubtreeDelete(ctx context.Context, jobID uuid.UUID, budget time.Duration) (DeleteResult, error) {
	started := clock.Now()
	for {
		progress, err := s.deleter.DeleteSubtreeStep(ctx, jobID, descendantDeleteEvent)
		if err != nil {
			slog.ErrorContext(ctx, "node.subtree_delete.step_failed", slog.String("err", err.Error()), slog.String("job_id", jobID.String()))
			return DeleteResult{JobID: jobID, Deleted: 0, State: node.SubtreeDeleteRunning},
				fmt.Errorf("run a step of delete job %s: %w", jobID, err)
		}
		if progress.Done {
			return DeleteResult{JobID: jobID, Deleted: progress.Deleted, State: node.SubtreeDeleteFinished}, nil
		}
		if budget > 0 && clock.Since(started) >= budget {
			return DeleteResult{JobID: jobID, Deleted: progress.Deleted, State: node.SubtreeDeleteRunning}, nil
		}
	}
}

// descendantDeleteEvent builds the node.delete ledger event of one deleted
// descendant from the staged event of the deleted root.
func descendantDeleteEvent(template json.RawMessage, deleted node.DeletedNode) (json.RawMessage, error) {
	payload, err := audit.DescendantDeleteEvent(template, audit.Entity{
		Type: "node", NodeType: deleted.NodeType, ID: deleted.ID, Identifier: "", Name: deleted.Name,
	})
	if err != nil {
		slog.Error("node.subtree_delete.event_failed", slog.String("err", err.Error()), slog.String("node_id", deleted.ID.String()))
		return nil, fmt.Errorf("build the delete event of node %s: %w", deleted.ID, err)
	}
	return payload, nil
}
