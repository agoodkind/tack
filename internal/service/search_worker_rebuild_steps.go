package service

import (
	"context"
	"errors"
	"fmt"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildCreate creates the empty full-replacement target, or splits the
// source after claims pause and every earlier lease expires.
func (w *SearchWorker) rebuildCreate(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	if rebuild.Mode == searchdomain.ReplacementFull {
		if err := w.engineStep(ctx, func(operation context.Context) error {
			return w.ports.Replacer.CreateReplacement(operation, rebuild.TargetIndex, rebuild.PrimaryShards, rebuild.RoutingShards, rebuild.Replicas)
		}); err != nil {
			return w.settleEngine(ctx, work, "create replacement index "+rebuild.TargetIndex, err)
		}
		switch err := w.waitGreen(ctx, work, rebuild.TargetIndex); {
		case errors.Is(err, searchdomain.ErrIndexNotReady):
			return w.targetNotGreen(ctx, work, rebuild, err)
		case err != nil:
			return w.settleEngine(ctx, work, "wait for green replacement index "+rebuild.TargetIndex, err)
		}
		next := rebuild
		next.State, next.ScanCursor, next.ScanComplete = searchdomain.RebuildCopying, "", false
		return w.advanceRebuild(ctx, work, rebuild, next)
	}
	if paused, err := w.pauseClaims(ctx, work, rebuild); !paused || err != nil {
		return err
	}
	switch err := w.splitSource(ctx, work, rebuild); {
	case errors.Is(err, searchdomain.ErrIndexNotReady):
		return w.targetNotGreen(ctx, work, rebuild, err)
	case err != nil:
		return w.failRebuild(ctx, work, rebuild, "split index "+rebuild.SourceIndex, err)
	}
	next := rebuild
	next.State, next.Paused, next.ScanComplete = searchdomain.RebuildCopying, false, true
	return w.advanceRebuild(ctx, work, rebuild, next)
}

// splitSource blocks source writes, splits the source into the target,
// clears the block the target inherits, waits for green health, and restores
// source writes. The public alias selects the readable source during every
// operation. A target that is not green within the claim lease returns
// [searchdomain.ErrIndexNotReady] with source writes still blocked. Claims
// stay paused, and the next claim repeats the split steps until the pause
// limit ends.
func (w *SearchWorker) splitSource(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	steps := []func(context.Context) error{
		func(operation context.Context) error {
			return w.ports.Replacer.SetWriteBlock(operation, rebuild.SourceIndex, true)
		},
		func(operation context.Context) error {
			return w.ports.Replacer.SplitIndex(operation, rebuild.SourceIndex, rebuild.TargetIndex, rebuild.PrimaryShards, rebuild.Replicas)
		},
		func(operation context.Context) error {
			return w.ports.Replacer.SetWriteBlock(operation, rebuild.TargetIndex, false)
		},
	}
	for _, step := range steps {
		if err := w.engineStep(ctx, step); err != nil {
			return err
		}
	}
	if err := w.waitGreen(ctx, work, rebuild.TargetIndex); err != nil {
		return rebuildStepError{operation: "wait for green split target " + rebuild.TargetIndex, err: err}
	}
	return w.engineStep(ctx, func(operation context.Context) error {
		return w.ports.Replacer.SetWriteBlock(operation, rebuild.SourceIndex, false)
	})
}

// rebuildSwitch pauses claims, waits for earlier leases, refreshes the
// target, and switches the public alias to the target in one atomic alias
// request. After an uncertain alias result, the step reads the alias.
func (w *SearchWorker) rebuildSwitch(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) error {
	if paused, err := w.pauseClaims(ctx, work, rebuild); !paused || err != nil {
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
	if err := w.moveAlias(ctx, rebuild); err != nil {
		return w.settleEngine(ctx, work, "switch public alias to "+rebuild.TargetIndex, err)
	}
	if err := w.ports.Rebuilds.CompleteSwitch(ctx, work, rebuild); err != nil {
		return w.settle(ctx, work, "record switched index "+rebuild.TargetIndex, err)
	}
	return w.yield(ctx, work)
}

// moveAlias switches the alias when it still selects the source. An alias
// that already selects the target needs no request. Any third target is a
// coordination error.
func (w *SearchWorker) moveAlias(ctx context.Context, rebuild searchdomain.Rebuild) error {
	current, err := w.ports.Replacer.PublicAliasTarget(ctx)
	if err != nil {
		return rebuildStepError{operation: "read public alias", err: err}
	}
	switch current {
	case rebuild.TargetIndex:
		return nil
	case rebuild.SourceIndex:
		switchErr := w.engineStep(ctx, func(operation context.Context) error {
			return w.ports.Replacer.SwitchPublicAlias(operation, rebuild.SourceIndex, rebuild.TargetIndex)
		})
		if switchErr == nil {
			return nil
		}
		after, readErr := w.ports.Replacer.PublicAliasTarget(ctx)
		if readErr == nil && after == rebuild.TargetIndex {
			return nil
		}
		return switchErr
	default:
		return fmt.Errorf("public alias selects %s, neither source %s nor target %s", current, rebuild.SourceIndex, rebuild.TargetIndex)
	}
}

// rebuildStepError adds operation context to one failed replacement step.
// The caller logs it once.
type rebuildStepError struct {
	operation string
	err       error
}

func (e rebuildStepError) Error() string { return e.operation + ": " + e.err.Error() }
func (e rebuildStepError) Unwrap() error { return e.err }

// pauseClaims records the durable pause and waits until every lease granted
// before it has expired. It reports whether the step may continue.
func (w *SearchWorker) pauseClaims(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild) (bool, error) {
	if !rebuild.Paused {
		next := rebuild
		next.Paused, next.PausedAt = true, w.clock.Now().UTC()
		return false, w.advanceRebuild(ctx, work, rebuild, next)
	}
	if w.clock.Now().Before(rebuild.PausedAt.Add(w.settings.Lease)) {
		return false, w.waitRebuild(ctx, work)
	}
	return true, nil
}

// waitGreen waits for green health of index until one operation timeout
// before the claim lease ends. That remainder lets the step finish its later
// operations and release the claim. It returns [searchdomain.ErrIndexNotReady]
// when the index is not green by then.
func (w *SearchWorker) waitGreen(ctx context.Context, work searchdomain.Work, index string) error {
	wait := work.LeaseUntil.Sub(w.clock.Now()) - w.settings.OperationTimeout
	if wait <= 0 {
		return searchdomain.ErrIndexNotReady
	}
	if err := w.ports.Replacer.WaitGreen(ctx, index, wait); err != nil {
		return rebuildStepError{operation: fmt.Sprintf("wait %s for green health", wait), err: err}
	}
	return nil
}

// targetNotGreen handles a target that is not green. Before the pause limit
// has passed since the replacement's PausedAt, it releases the claim for a
// later retry. After the limit, it records the replacement as failed. The
// next claim of the failed replacement restores source writes, deletes the
// target, and resumes claims. A split or switch sets PausedAt when it pauses
// claims. A full replacement never pauses claims, and its PausedAt is the
// begin time. For a full replacement, the limit also counts the wait for the
// first claim of the rebuild work, and that claim creates the target.
func (w *SearchWorker) targetNotGreen(ctx context.Context, work searchdomain.Work, rebuild searchdomain.Rebuild, cause error) error {
	if w.clock.Since(rebuild.PausedAt) < w.settings.ReplacementPauseLimit {
		return w.waitRebuild(ctx, work)
	}
	return w.failRebuild(ctx, work, rebuild, "wait for green target "+rebuild.TargetIndex, cause)
}
