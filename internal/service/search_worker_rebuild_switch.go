package service

import (
	"context"
	"errors"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildSwitch runs the second copy check after pauseClaims drains live and copy
// leases. The pause blocks live and copy claims; rebuild claims remain available.
// When the second check finds pending copies, switchWaitsForCopies clears Paused
// and returns before the alias switch. On the successful path, live and copy
// claims stay paused through the alias switch until CompleteSwitch clears Paused.
// During the pause, RecordMirrorMiss stores an abandoned mirror write as a live
// pending deadline for the replacement index without adding a copy item.
// After CompleteSwitch, live claims target the replacement index.
// The next live claim reads the deadline before resending the document.
func (w *SearchWorker) rebuildSwitch(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	if copying, err := w.switchWaitsForCopies(ctx, work, rebuild); copying || err != nil {
		return err
	}
	if paused, err := w.pauseClaims(ctx, work, rebuild); !paused || err != nil {
		return err
	}
	if copying, err := w.switchWaitsForCopies(ctx, work, rebuild); copying || err != nil {
		return err
	}
	if err := w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Writer.Refresh(operation, rebuild.TargetIndex)
	}); err != nil {
		return w.settleEngine(ctx, work, "refresh replacement index "+rebuild.TargetIndex, err)
	}
	switch err := w.waitGreen(ctx, work, rebuild.TargetIndex); {
	case errors.Is(err, searchdomain.ErrIndexNotReady):
		return w.targetNotGreen(ctx, work, rebuild, err)
	case err != nil:
		return w.settleEngine(ctx, work, "wait for green replacement index "+rebuild.TargetIndex, err)
	}
	if err := w.recordAliasSwitch(ctx, rebuild, func(ctx context.Context) error { return w.moveAlias(ctx, rebuild) }); err != nil {
		return w.settleEngine(ctx, work, "switch public alias to "+rebuild.TargetIndex, err)
	}
	if err := w.ports.Rebuilds.CompleteSwitch(ctx, work, rebuild); err != nil {
		return w.settle(ctx, work, "record switched index "+rebuild.TargetIndex, err)
	}
	return w.yield(ctx, work)
}

// Pending copies need active claims before the replacement can switch its alias.
func (w *SearchWorker) switchWaitsForCopies(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) (bool, error) {
	pending, err := w.ports.Rebuilds.CopiesPending(ctx)
	if err != nil {
		return true, w.settle(ctx, work, "read pending replacement copies", err)
	}
	if !pending {
		return false, nil
	}
	if !rebuild.Paused {
		return true, w.waitRebuild(ctx, work)
	}
	next := rebuild
	next.Paused = false
	return true, w.advanceRebuild(ctx, work, rebuild, next)
}
