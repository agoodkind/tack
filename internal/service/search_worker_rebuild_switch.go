package service

import (
	"context"
	"errors"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildSwitch checks switchWaitsForCopies before calling pauseClaims. pauseClaims
// pauses claims and waits for every earlier lease to expire. rebuildSwitch checks
// switchWaitsForCopies again after that wait. rebuildSwitch proceeds only when both
// checks return false. Every live and copy lease has expired at the second check.
//
// rebuildSwitch keeps claims paused from the second check until the alias switch.
// RecordMirrorMiss does not write a copy item while claims are paused.
// RecordMirrorMiss stores a live class pending write record for the replacement
// index instead. The live claim after the switch waits for that record deadline
// before it writes the page.
//
// rebuildSwitch refreshes the target, waits for green health, and switches the public
// alias to the target in one atomic alias request. After an uncertain alias result,
// rebuildSwitch reads the alias. rebuildSwitch records the switch and yields.
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

// switchWaitsForCopies reads CopiesPending and returns false when copies are not pending.
// When copies are pending and claims are active, switchWaitsForCopies calls
// waitRebuild to delay the replacement claim and returns true.
// When copies are pending and claims are paused, switchWaitsForCopies calls
// advanceRebuild to record the replacement with Paused false and returns true.
// Workers then resume claims and run the copies on later claims.
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
