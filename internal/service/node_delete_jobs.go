package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// subtreeDeleteJobPage bounds the job records one read of
// ResumeSubtreeDeletes lists.
const subtreeDeleteJobPage = 100

// finishedDeleteRetention is how long DeleteStatus can read a finished job
// before ResumeSubtreeDeletes clears its record.
const finishedDeleteRetention = time.Hour

// DeleteStatus reads one subtree delete job. A job ID without a record
// returns [domain.ErrNotFound].
func (s *NodeService) DeleteStatus(ctx context.Context, jobID uuid.UUID) (DeleteResult, error) {
	job, err := s.deleter.SubtreeDelete(ctx, jobID)
	none := DeleteResult{JobID: jobID, Deleted: 0, Moved: 0, State: node.SubtreeDeleteRunning}
	if err != nil {
		slog.ErrorContext(ctx, "node.subtree_delete.status_failed", slog.String("err", err.Error()), slog.String("job_id", jobID.String()))
		return none, fmt.Errorf("read delete job %s: %w", jobID, err)
	}
	if job == nil {
		return none, fmt.Errorf("delete job %s: %w", jobID, domain.ErrNotFound)
	}
	return DeleteResult{JobID: jobID, Deleted: job.Deleted, Moved: job.Moved, State: job.State()}, nil
}

// ResumeSubtreeDeletes finishes every running subtree delete job without a
// step in the last staleAfter, and returns the number of jobs it finished. A
// running job without a recent step has no runner: its request used up
// deleteRequestBudget, stopped, or crashed. The pass also clears the records
// of jobs that finished more than finishedDeleteRetention ago.
func (s *NodeService) ResumeSubtreeDeletes(ctx context.Context, staleAfter time.Duration) (int, error) {
	now := clock.Now()
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
			resumed, err := s.resumeJob(ctx, job, now, staleAfter)
			if err != nil {
				return finished, err
			}
			if resumed {
				finished++
			}
		}
		if len(jobs) < subtreeDeleteJobPage {
			return finished, nil
		}
	}
}

// resumeJob clears an expired finished job or finishes a stale running job.
// It reports whether it finished a running job.
func (s *NodeService) resumeJob(ctx context.Context, job *node.SubtreeDeleteJob, now time.Time, staleAfter time.Duration) (bool, error) {
	if job.Finished() {
		if now.Sub(job.FinishedAt) < finishedDeleteRetention {
			return false, nil
		}
		if err := s.deleter.ClearSubtreeDelete(ctx, job.ID); err != nil {
			slog.ErrorContext(ctx, "node.subtree_delete.clear_failed", slog.String("err", err.Error()), slog.String("job_id", job.ID.String()))
			return false, fmt.Errorf("clear finished delete job %s: %w", job.ID, err)
		}
		return false, nil
	}
	if job.UpdatedAt.After(now.Add(-staleAfter)) {
		return false, nil
	}
	result, err := s.runSubtreeDelete(ctx, job.ID, 0)
	if err != nil {
		return false, err
	}
	telemetry.L(ctx).InfoContext(ctx, "node.subtree_delete.resumed", slog.String("job_id", job.ID.String()),
		slog.String("root_id", job.RootID.String()), slog.Int("deleted", result.Deleted))
	return true, nil
}
