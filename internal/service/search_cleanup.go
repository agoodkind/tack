package service

import (
	"context"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// cleanupSlice retires at most 100 obsolete page documents. Each retirement
// replaces the document with a text-free retired record at the current
// generation. The slice refreshes the target and mirror index before it
// checkpoints the batch. A session opened after the checkpoint then reads no
// retired page as active. A retried batch rewrites the same retired records.
// The store clears only the accepted contiguous prefix.
func (w *SearchWorker) cleanupSlice(ctx context.Context, work searchdomain.Work, budget *sliceBudget) error {
	batch, err := w.ports.Store.Documents(ctx, work, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "read obsolete search documents", err)
	}
	accepted := 0
	var writeErr error
	if len(batch.Documents) > 0 {
		operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
		accepted, writeErr = w.ports.Writer.Retire(operationContext, searchdomain.RetirementIntent{Work: work, Documents: batch.Documents})
		cancel()
		budget.record(accepted, 0)
	}
	if accepted > 0 {
		if err := w.refreshRetired(ctx, work); err != nil {
			return w.settleEngine(ctx, work, "refresh search index after retirement", err)
		}
	}
	done := writeErr == nil && batch.Done
	if err := w.ports.Store.CompleteRetirement(ctx, work, batch.Documents[:accepted], done); err != nil {
		return w.settle(ctx, work, "checkpoint retired search documents", err)
	}
	if writeErr != nil {
		return w.settleEngine(ctx, work, "retire obsolete search documents", writeErr)
	}
	if !done {
		return w.yield(ctx, work)
	}
	return nil
}

// refreshRetired refreshes the target index of work and, during a
// replacement, the mirror index that also received the retirements.
func (w *SearchWorker) refreshRetired(ctx context.Context, work searchdomain.Work) error {
	for _, index := range []string{work.Target, work.Mirror} {
		if index == "" {
			continue
		}
		operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
		err := w.ports.Writer.Refresh(operationContext, index)
		cancel()
		if err != nil {
			wrapped := fmt.Errorf("refresh index %s for node %s: %w", index, work.NodeID, err)
			telemetry.L(ctx).ErrorContext(ctx, "search.cleanup.refresh_failed", slog.String("err", wrapped.Error()),
				slog.String("node_id", work.NodeID.String()), slog.String("index", index))
			return wrapped
		}
	}
	return nil
}
