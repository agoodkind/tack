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

// subtreeDeleteJobPage bounds the job records one read of
// ResumeSubtreeDeletes lists.
const subtreeDeleteJobPage = 100

// Delete removes the node and every hierarchy descendant of the node, and
// returns the number of deleted nodes. NodeType metadata decides which nodes
// are descendants. The delete stores a job record in FoundationDB and then
// runs bounded steps of the job until the root is gone. Each deleted node
// writes one node.delete ledger event in the transaction that deletes it.
// When the caller stops before the root is gone, ResumeSubtreeDeletes
// finishes the job.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) (int, error) {
	ctx, span := telemetry.StartSpan(ctx, "service.node.delete",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", nodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", nodeID.String()), slog.String("actor_id", actorID.String()))

	existing, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("read node %s to delete it: %w", nodeID, err)
	}
	if existing == nil {
		return 0, domain.ErrNotFound
	}
	if err := audit.StageStateChange(ctx, audit.VerbNodeDelete, audit.Entity{
		Type: "node", NodeType: existing.NodeType, ID: nodeID, Identifier: "", Name: existing.Name,
	}); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("stage the audit row for deleting %s: %w", nodeID, err)
	}
	template, _ := auditintent.Pending(ctx)
	jobID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("create the delete job id for node %s: %w", nodeID, err)
	}
	job := &node.SubtreeDeleteJob{
		ID: jobID, OrgID: existing.OrgID, RootID: nodeID, AuditTemplate: template,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := s.deleter.StartSubtreeDelete(ctx, job); err != nil {
		slog.ErrorContext(ctx, "node.delete_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("start the delete of node %s: %w", nodeID, err)
	}
	deleted, err := s.runSubtreeDelete(ctx, jobID)
	if err != nil {
		return 0, err
	}
	telemetry.L(ctx).InfoContext(ctx, "node.deleted", slog.Int("deleted", deleted), slog.String("job_id", jobID.String()))
	return deleted, nil
}

// ResumeSubtreeDeletes finishes every subtree delete job without a step in
// the last staleAfter, and returns the number of finished jobs. A job
// without a recent step lost its runner, for example to a process crash.
func (s *NodeService) ResumeSubtreeDeletes(ctx context.Context, staleAfter time.Duration) (int, error) {
	cutoff := clock.Now().Add(-staleAfter)
	after := uuid.Nil
	finished := 0
	for {
		jobs, err := s.deleter.SubtreeDeletes(ctx, after, subtreeDeleteJobPage)
		if err != nil {
			slog.ErrorContext(ctx, "node.subtree_delete.resume_failed", slog.String("err", err.Error()))
			return finished, fmt.Errorf("list subtree delete jobs after %s: %w", after, err)
		}
		for _, job := range jobs {
			after = job.ID
			if job.UpdatedAt.After(cutoff) {
				continue
			}
			deleted, err := s.runSubtreeDelete(ctx, job.ID)
			if err != nil {
				return finished, err
			}
			finished++
			telemetry.L(ctx).InfoContext(ctx, "node.subtree_delete.resumed", slog.String("job_id", job.ID.String()),
				slog.String("root_id", job.RootID.String()), slog.Int("deleted", deleted))
		}
		if len(jobs) < subtreeDeleteJobPage {
			return finished, nil
		}
	}
}

// runSubtreeDelete runs steps of one job until the job reports Done, and
// returns the job's deleted node count.
func (s *NodeService) runSubtreeDelete(ctx context.Context, jobID uuid.UUID) (int, error) {
	for {
		progress, err := s.deleter.DeleteSubtreeStep(ctx, jobID, descendantDeleteEvent)
		if err != nil {
			slog.ErrorContext(ctx, "node.subtree_delete.step_failed", slog.String("err", err.Error()), slog.String("job_id", jobID.String()))
			return 0, fmt.Errorf("run a step of delete job %s: %w", jobID, err)
		}
		if progress.Done {
			return progress.Deleted, nil
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
