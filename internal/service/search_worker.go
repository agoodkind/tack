package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// SearchWorkerPorts are the production boundaries one search worker uses.
// Rollouts records access policy rollout state. Sessions reports whether a
// session still reads the previous access version. Documents reads the
// stored access of page documents for verification. Rebuilds records the
// index replacement state. Replacer performs the OpenSearch operations of
// the index replacement.
type SearchWorkerPorts struct {
	Store     searchdomain.WorkStore
	Reader    searchdomain.ContentReader
	Access    searchdomain.AccessReader
	Writer    searchdomain.PageWriter
	Rollouts  searchdomain.AccessRolloutStore
	Sessions  searchdomain.AccessVersionSessions
	Documents searchdomain.DocumentAccessReader
	Rebuilds  searchdomain.RebuildStore
	Replacer  searchdomain.IndexReplacer
}

// SearchWorker processes bounded durable search work of every class.
type SearchWorker struct {
	ports    SearchWorkerPorts
	clock    clock.Clock
	owner    string
	settings config.SearchWorkerSettings
	schedule []searchdomain.WorkClass
	position int
}

// NewSearchWorker constructs one worker identity. offset staggers the first
// class each worker tries so concurrent workers start on different classes.
func NewSearchWorker(ports SearchWorkerPorts, source clock.Clock, settings config.SearchWorkerSettings, offset int) (*SearchWorker, error) {
	schedule, err := weightedSchedule(settings.ClassWeights)
	if err != nil {
		return nil, err
	}
	return &SearchWorker{
		ports: ports, clock: source, owner: uuid.NewString(), settings: settings,
		schedule: schedule, position: offset % len(schedule),
	}, nil
}

// RunSlice claims one work item in class rotation and processes one bounded
// slice of it. It reports whether it claimed work.
func (w *SearchWorker) RunSlice(ctx context.Context) (bool, error) {
	work, claimed, err := w.claimNext(ctx)
	if err != nil || !claimed {
		return false, err
	}
	return true, w.Process(ctx, work)
}

// Process runs one bounded slice of a claimed work item and logs its page
// count, encoded bytes, duration, and work age.
func (w *SearchWorker) Process(ctx context.Context, work searchdomain.Work) error {
	budget := newSliceBudget(w.clock, w.settings)
	err := w.processClass(ctx, work, budget)
	telemetry.L(ctx).DebugContext(ctx, "search.worker.slice_completed",
		slog.String("class", string(work.Class)), slog.String("node_id", work.NodeID.String()),
		slog.Int64("generation", work.Generation), slog.String("index", work.Target),
		slog.Int("pages", budget.pages), slog.Int("encoded_bytes", budget.bytes),
		slog.Duration("duration", w.clock.Since(budget.started)),
		slog.Duration("work_age", budget.started.Sub(work.EnqueuedAt)))
	return err
}

func (w *SearchWorker) processClass(ctx context.Context, work searchdomain.Work, budget *sliceBudget) error {
	switch work.Class {
	case searchdomain.WorkClassLive, searchdomain.WorkClassCopy:
		return w.contentSlice(ctx, work, budget)
	case searchdomain.WorkClassAccess:
		return w.accessSlice(ctx, work, budget)
	case searchdomain.WorkClassCleanup:
		return w.cleanupSlice(ctx, work, budget)
	case searchdomain.WorkClassRescan:
		return w.rescanSlice(ctx, work)
	case searchdomain.WorkClassRollout:
		return w.rolloutSlice(ctx, work)
	case searchdomain.WorkClassRebuild:
		return w.rebuildSlice(ctx, work)
	default:
		return w.settle(ctx, work, "process search work", fmt.Errorf("unknown work class %q", work.Class))
	}
}

func (w *SearchWorker) claimNext(ctx context.Context) (searchdomain.Work, bool, error) {
	defer func() { w.position = (w.position + 1) % len(w.schedule) }()
	var none searchdomain.Work
	tried := make(map[searchdomain.WorkClass]struct{}, len(w.schedule))
	for offset := range w.schedule {
		class := w.schedule[(w.position+offset)%len(w.schedule)]
		if _, seen := tried[class]; seen {
			continue
		}
		tried[class] = struct{}{}
		work, err := w.ports.Store.Claim(ctx, class, w.owner, w.settings.Lease)
		switch {
		case err == nil:
			return work, true, nil
		case errors.Is(err, searchdomain.ErrNoWork):
			continue
		case errors.Is(err, searchdomain.ErrNoServingIndex):
			telemetry.L(ctx).DebugContext(ctx, "search.worker.index_pending", slog.String("class", string(class)))
			return none, false, nil
		default:
			wrapped := fmt.Errorf("claim %s search work: %w", class, err)
			telemetry.L(ctx).ErrorContext(ctx, "search.worker.claim_failed", slog.String("err", wrapped.Error()), slog.String("class", string(class)))
			return none, false, loggedWorkerError{err: wrapped}
		}
	}
	return none, false, nil
}

type loggedWorkerError struct{ err error }

func (e loggedWorkerError) Error() string { return e.err.Error() }
func (e loggedWorkerError) Unwrap() error { return e.err }
