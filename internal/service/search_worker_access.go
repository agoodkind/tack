package service

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// accessSlice processes one bounded access-only step. The pages phase
// compiles current access and records it in FoundationDB before it sends
// any page update. The phase then updates at most 100 issued pages of every
// unretired revision with only search_generation and access. It never reads
// text and never invokes the model. When the recorded access changed, the
// dependents phase schedules access work for at most 100 dependents per
// slice. A deleted node starts in the dependents phase and schedules its
// recorded counterparts.
func (w *SearchWorker) accessSlice(ctx context.Context, work searchdomain.Work, budget *sliceBudget) error {
	if work.Phase != searchdomain.PhaseDependents {
		access, err := w.ports.Access.Compile(ctx, work)
		if err != nil {
			return w.settle(ctx, work, "compile search access", err)
		}
		plan, err := w.ports.Store.BeginAccess(ctx, work, access)
		if err != nil {
			return w.settle(ctx, work, "record compiled search access", err)
		}
		if plan.Finished {
			return nil
		}
		work = plan.Work
		if work.Phase == searchdomain.PhasePages {
			return w.accessPages(ctx, work, access, budget)
		}
	}
	page, err := w.ports.Access.Dependents(ctx, work, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "list search access dependents", err)
	}
	if err := w.ports.Store.ScheduleDependents(ctx, work, page); err != nil {
		return w.settle(ctx, work, "schedule search access dependents", err)
	}
	if !page.Done {
		return w.yield(ctx, work)
	}
	return nil
}

// accessPages updates one bounded batch of issued pages with access and
// checkpoints the accepted prefix.
func (w *SearchWorker) accessPages(ctx context.Context, work searchdomain.Work, access node.SearchAccess, budget *sliceBudget) error {
	batch, err := w.ports.Store.Documents(ctx, work, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "read search access documents", err)
	}
	accepted := 0
	var writeErr error
	if len(batch.Documents) > 0 {
		operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
		accepted, writeErr = w.ports.Writer.UpdateAccess(operationContext, searchdomain.AccessIntent{
			Work: work, Documents: batch.Documents, Access: access,
		})
		cancel()
		budget.record(accepted, 0)
	}
	checkpoint := searchdomain.AccessCheckpoint{
		Access: access, Accepted: batch.Documents[:accepted], PagesDone: writeErr == nil && batch.Done,
	}
	if err := w.ports.Store.CompleteAccess(ctx, work, checkpoint); err != nil {
		return w.settle(ctx, work, "checkpoint search access documents", err)
	}
	if writeErr != nil {
		return w.settleEngine(ctx, work, "update search access documents", writeErr)
	}
	return w.yield(ctx, work)
}
