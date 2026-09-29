package service

import (
	"context"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// cleanupSlice retires at most 100 obsolete page documents. Each retirement
// replaces the document with a text-free retired record at the current
// generation. The store clears only the accepted contiguous prefix.
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
