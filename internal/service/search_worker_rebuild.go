package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// rebuildSlice runs one step of the index replacement. Each step reads the
// durable state, performs the engine and FoundationDB operations of the
// current replacement state, and checkpoints the resulting state. The step
// context ends when the claim lease ends. The worker cancels every operation
// of the step that still runs when another worker can claim the replacement,
// and every step is safe to repeat.
func (w *SearchWorker) rebuildSlice(ctx context.Context, work searchdomain.Work) error {
	leaseContext, cancel := context.WithTimeout(ctx, work.LeaseUntil.Sub(w.clock.Now()))
	defer cancel()
	return w.rebuildState(leaseContext, work)
}

// rebuildState runs the step of the current replacement state.
func (w *SearchWorker) rebuildState(ctx context.Context, work searchdomain.Work) error {
	rebuild, found, err := w.ports.Rebuilds.CurrentRebuild(ctx)
	if err != nil {
		return w.settle(ctx, work, "read index replacement", err)
	}
	if !found {
		return w.settle(ctx, work, "advance index replacement", searchdomain.ErrWorkChanged)
	}
	switch rebuild.State {
	case searchdomain.RebuildCreating:
		return w.rebuildCreate(ctx, work, rebuild)
	case searchdomain.RebuildCopying:
		return w.rebuildCopy(ctx, work, rebuild)
	case searchdomain.RebuildVerifying:
		return w.rebuildVerify(ctx, work, rebuild)
	case searchdomain.RebuildSwitching:
		return w.rebuildSwitch(ctx, work, rebuild)
	case searchdomain.RebuildRetiring:
		return w.rebuildRetire(ctx, work, rebuild)
	case searchdomain.RebuildFailed:
		return w.rebuildRecover(ctx, work, rebuild)
	}
	return w.settle(ctx, work, "advance index replacement", fmt.Errorf("unknown replacement state %q", rebuild.State))
}

// rebuildCopy schedules copies for one page of FoundationDB nodes. After the
// scan finishes it waits until every copy item completes.
func (w *SearchWorker) rebuildCopy(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	if !rebuild.ScanComplete {
		if err := w.ports.Rebuilds.CopyNextNodes(ctx, work, rebuild); err != nil {
			return w.settle(ctx, work, "schedule replacement copies", err)
		}
		return w.yield(ctx, work)
	}
	pending, err := w.ports.Rebuilds.CopiesPending(ctx)
	if err != nil {
		return w.settle(ctx, work, "read pending replacement copies", err)
	}
	if pending {
		return w.waitRebuild(ctx, work)
	}
	next := rebuild
	next.State, next.VerifyCursor = searchdomain.RebuildVerifying, ""
	return w.advanceRebuild(ctx, work, rebuild, next)
}

// rebuildVerify reads one batch of exact current pages from the target.
func (w *SearchWorker) rebuildVerify(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	batch, err := w.ports.Rebuilds.VerifyRebuildDocuments(ctx, work, rebuild, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "read replacement verification documents", err)
	}
	ids := make([]string, 0, len(batch.Documents))
	for _, document := range batch.Documents {
		ids = append(ids, document.Document.DocumentID)
	}
	operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
	states, err := w.ports.Documents.DocumentAccess(operationContext, rebuild.TargetIndex, ids)
	cancel()
	if err != nil {
		return w.settleEngine(ctx, work, "read replacement page documents", err)
	}
	waiting, err := w.ports.Rebuilds.CompleteRebuildVerify(ctx, work, rebuild, batch, states)
	if err != nil {
		return w.settle(ctx, work, "checkpoint replacement verification", err)
	}
	if waiting {
		return w.waitRebuild(ctx, work)
	}
	return w.yield(ctx, work)
}

// rebuildRetire deletes the old index after every session that reads it has
// ended, then finishes the replacement. A missing old index skips the write
// block. An earlier attempt that deleted the index and then failed to record
// the finish leaves the index missing.
func (w *SearchWorker) rebuildRetire(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	remaining, err := w.ports.Rebuilds.RetireIndexSessions(ctx, rebuild.SourceIndex, searchBatchLimit)
	if err != nil {
		return w.settle(ctx, work, "retire sessions of index "+rebuild.SourceIndex, err)
	}
	if remaining {
		return w.waitRebuild(ctx, work)
	}
	if err := w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Replacer.SetWriteBlock(operation, rebuild.SourceIndex, true)
	}); err != nil && !errors.Is(err, searchdomain.ErrIndexNotFound) {
		return w.settleEngine(ctx, work, "block writes to retiring index "+rebuild.SourceIndex, err)
	}
	if err := w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Replacer.DeleteIndex(operation, rebuild.SourceIndex)
	}); err != nil {
		return w.settleEngine(ctx, work, "delete retiring index "+rebuild.SourceIndex, err)
	}
	if err := w.ports.Rebuilds.FinishRebuild(ctx, work, rebuild); err != nil {
		return w.settle(ctx, work, "finish index replacement", err)
	}
	return nil
}

// rebuildRecover restores source writes, deletes the failed target, and ends
// the replacement. The public alias still selects the source in this state.
func (w *SearchWorker) rebuildRecover(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	if err := w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Replacer.SetWriteBlock(operation, rebuild.SourceIndex, false)
	}); err != nil {
		return w.settleEngine(ctx, work, "restore writes to index "+rebuild.SourceIndex, err)
	}
	if err := w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Replacer.DeleteIndex(operation, rebuild.TargetIndex)
	}); err != nil {
		return w.settleEngine(ctx, work, "delete failed target "+rebuild.TargetIndex, err)
	}
	if err := w.ports.Rebuilds.FinishRebuild(ctx, work, rebuild); err != nil {
		return w.settle(ctx, work, "finish failed index replacement", err)
	}
	return nil
}

// advanceRebuild checkpoints next and yields the claim.
func (w *SearchWorker) advanceRebuild(ctx context.Context, work searchdomain.Work, read, next searchdomain.Rebuild) error {
	if err := w.ports.Rebuilds.AdvanceRebuild(ctx, work, read, next); err != nil {
		return w.settle(ctx, work, "advance index replacement to "+string(next.State), err)
	}
	return w.yield(ctx, work)
}

// failRebuild records failure in the durable state. The failed state
// restores source writes and deletes the target on the next claim.
func (w *SearchWorker) failRebuild(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild, operation string, cause error) error {
	wrapped := fmt.Errorf("%s for index replacement %s: %w", operation, rebuild.ID, cause)
	telemetry.L(ctx).ErrorContext(ctx, "search.rebuild.step_failed", slog.String("err", wrapped.Error()),
		slog.String("rebuild_id", rebuild.ID.String()), slog.String("state", string(rebuild.State)))
	next := rebuild
	next.State, next.Paused, next.Failure = searchdomain.RebuildFailed, false, wrapped.Error()
	return w.advanceRebuild(ctx, work, rebuild, next)
}

// engineStep runs one engine operation under the operation timeout.
func (w *SearchWorker) engineStep(ctx context.Context, step func(context.Context) error) error {
	operationContext, cancel := context.WithTimeout(ctx, w.settings.OperationTimeout)
	defer cancel()
	return step(operationContext)
}

// waitRebuild delays the next claim of the replacement without recording a
// failure.
func (w *SearchWorker) waitRebuild(ctx context.Context, work searchdomain.Work) error {
	err := w.ports.Rebuilds.WaitRebuild(ctx, work)
	if err == nil || errors.Is(err, searchdomain.ErrWorkChanged) {
		return nil
	}
	wrapped := fmt.Errorf("delay index replacement: %w", err)
	telemetry.L(ctx).ErrorContext(ctx, "search.worker.rebuild_wait_failed", slog.String("err", wrapped.Error()))
	return loggedWorkerError{err: wrapped}
}
