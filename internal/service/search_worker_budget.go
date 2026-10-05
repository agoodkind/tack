package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// searchBatchLimit bounds the issued IDs, dependents, or scanned nodes one
// slice processes.
const searchBatchLimit = 100

// scheduledClasses lists every class a worker rotates through, in the order
// that breaks weight ties.
var scheduledClasses = searchdomain.ScheduledWorkClasses()

// sliceBudget counts the pages and encoded bytes of one slice against the
// configured bounds and the injected clock.
type sliceBudget struct {
	clock    clock.Clock
	settings config.SearchWorkerSettings
	started  time.Time
	pages    int
	bytes    int
}

func newSliceBudget(source clock.Clock, settings config.SearchWorkerSettings) *sliceBudget {
	return &sliceBudget{clock: source, settings: settings, started: source.Now(), pages: 0, bytes: 0}
}

// spent reports whether the slice must start no further remote operation.
func (b *sliceBudget) spent() bool {
	return b.pages >= b.settings.MaxPages || b.bytes >= b.settings.MaxBytes || b.clock.Since(b.started) >= b.settings.SliceBudget
}

func (b *sliceBudget) record(pages, encodedBytes int) {
	b.pages += pages
	b.bytes += encodedBytes
}

// weightedSchedule interleaves the classes by weight with smooth weighted
// round robin. A class with weight w receives w turns in each rotation.
func weightedSchedule(weights map[string]int) ([]searchdomain.WorkClass, error) {
	total := 0
	for _, class := range scheduledClasses {
		weight, exists := weights[string(class)]
		if !exists || weight < 1 {
			return nil, fmt.Errorf("search work class %s requires a positive weight", class)
		}
		total += weight
	}
	if len(weights) != len(scheduledClasses) {
		return nil, errors.New("search work class weights name an unknown class")
	}
	current := make([]int, len(scheduledClasses))
	schedule := make([]searchdomain.WorkClass, 0, total)
	for range total {
		selected := 0
		for position, class := range scheduledClasses {
			current[position] += weights[string(class)]
			if current[position] > current[selected] {
				selected = position
			}
		}
		current[selected] -= total
		schedule = append(schedule, scheduledClasses[selected])
	}
	return slices.Clip(schedule), nil
}

// settle ends a slice after err. The failure counts toward the attempt limit
// of the work. Engine operations settle through settleEngine instead.
func (w *SearchWorker) settle(ctx context.Context, work searchdomain.Work, operation string, err error) error {
	return w.fail(ctx, work, operation, err, true, 0)
}

// settleEngine ends a slice after a failed OpenSearch operation. The failure
// never counts toward the attempt limit. OpenSearch continues a request after
// the worker stops waiting, and the next claim waits the operation timeout.
func (w *SearchWorker) settleEngine(ctx context.Context, work searchdomain.Work, operation string, err error) error {
	return w.fail(ctx, work, operation, err, false, w.settings.OperationTimeout)
}

// fail ends a slice after err. A changed or obsolete claim yields without
// recording a failure. A node without exactly one hierarchy parent is
// excluded from search, and the slice succeeds. Any other error records the
// failure and delays the next claim by at least retryAfter.
func (w *SearchWorker) fail(ctx context.Context, work searchdomain.Work, operation string, err error, counted bool, retryAfter time.Duration) error {
	if errors.Is(err, searchdomain.ErrWorkChanged) || errors.Is(err, searchdomain.ErrObsoleteWrite) {
		telemetry.L(ctx).InfoContext(ctx, "search.worker.work_changed", slog.String("node_id", work.NodeID.String()),
			slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation), slog.String("operation", operation))
		return w.yield(ctx, work)
	}
	wrapped := fmt.Errorf("%s for node %s: %w", operation, work.NodeID, err)
	if work.Excludable() && errors.Is(err, searchaccess.ErrNoHierarchyParent) && ctx.Err() == nil {
		return w.exclude(ctx, work, wrapped)
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.worker.slice_failed", slog.String("err", wrapped.Error()),
		slog.String("node_id", work.NodeID.String()), slog.String("class", string(work.Class)),
		slog.Int64("generation", work.Generation), slog.String("index", work.Target))
	if ctx.Err() != nil {
		return loggedWorkerError{err: wrapped}
	}
	failure := searchdomain.Failure{Message: wrapped.Error(), Counted: counted, RetryAfter: retryAfter}
	if releaseErr := w.ports.Store.Release(ctx, work, failure); releaseErr != nil && !errors.Is(releaseErr, searchdomain.ErrWorkChanged) {
		telemetry.L(ctx).ErrorContext(ctx, "search.worker.release_failed", slog.String("err", releaseErr.Error()),
			slog.String("node_id", work.NodeID.String()), slog.String("class", string(work.Class)))
	}
	return loggedWorkerError{err: wrapped}
}

// exclude excludes the node of work from search with cause as the reason.
// The store logs the exclusion. A changed claim ends the slice without an
// exclusion.
func (w *SearchWorker) exclude(ctx context.Context, work searchdomain.Work, cause error) error {
	err := w.ports.Store.Exclude(ctx, work, cause.Error())
	if err == nil || errors.Is(err, searchdomain.ErrWorkChanged) {
		return nil
	}
	wrapped := fmt.Errorf("exclude node %s from search after %s: %w", work.NodeID, cause.Error(), err)
	telemetry.L(ctx).ErrorContext(ctx, "search.worker.exclude_failed", slog.String("err", wrapped.Error()),
		slog.String("node_id", work.NodeID.String()), slog.String("class", string(work.Class)))
	return loggedWorkerError{err: wrapped}
}

// yield leaves the work and its checkpoint pending for the next claim.
func (w *SearchWorker) yield(ctx context.Context, work searchdomain.Work) error {
	err := w.ports.Store.Yield(ctx, work)
	if err == nil || errors.Is(err, searchdomain.ErrWorkChanged) {
		return nil
	}
	wrapped := fmt.Errorf("yield search work for node %s: %w", work.NodeID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.worker.yield_failed", slog.String("err", wrapped.Error()),
		slog.String("node_id", work.NodeID.String()), slog.String("class", string(work.Class)))
	return loggedWorkerError{err: wrapped}
}
