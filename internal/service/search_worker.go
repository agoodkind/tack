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
// the index replacement. Audit supplies the outbox and service actor.
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
	Audit     SearchAudit
}

// SearchWorker processes bounded durable search work of every class.
type SearchWorker struct {
	ports    SearchWorkerPorts
	clock    clock.Clock
	owner    string
	settings config.SearchWorkerSettings
	shares   *classShares
}

// NewSearchWorker constructs one worker identity with class time shares.
// offset rotates the class order used to break ties. Concurrent workers
// with different offsets start at different classes.
func NewSearchWorker(ports SearchWorkerPorts, source clock.Clock, settings config.SearchWorkerSettings, offset int) (*SearchWorker, error) {
	if ports.Audit.Outbox == nil || ports.Audit.Actor.ID == uuid.Nil {
		return nil, errors.New("search worker: set the audit outbox and service actor")
	}
	shares, err := newClassShares(settings.ClassWeights, settings.SliceBudget, offset)
	if err != nil {
		return nil, err
	}
	return &SearchWorker{ports: ports, clock: source, owner: uuid.NewString(), settings: settings, shares: shares}, nil
}

// RunSlice claims from the pending class with the least worker time per
// unit of weight and processes one bounded slice. It reports whether it
// claimed work.
func (w *SearchWorker) RunSlice(ctx context.Context) (bool, error) {
	work, claimed, err := w.claimNext(ctx)
	if err != nil || !claimed {
		return false, err
	}
	return true, w.Process(ctx, work)
}

// Process runs one bounded slice of a claimed work item. It measures elapsed
// wall time with the injected clock and charges that time to the class,
// including when the slice fails. It logs page count, encoded bytes,
// duration, class_weighted_time, and work age at Debug level.
func (w *SearchWorker) Process(ctx context.Context, work searchdomain.Work) error {
	budget := newSliceBudget(w.clock, w.settings)
	err := w.processClass(ctx, work, budget)
	duration := w.clock.Since(budget.started)
	weighted := w.shares.spend(work.Class, duration)
	telemetry.L(ctx).DebugContext(ctx, "search.worker.slice_completed",
		slog.String("class", string(work.Class)), slog.String("node_id", work.NodeID.String()),
		slog.Int64("generation", work.Generation), slog.String("index", work.Target),
		slog.Int("pages", budget.pages), slog.Int("encoded_bytes", budget.bytes),
		slog.Duration("duration", duration), slog.Duration("class_weighted_time", weighted),
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

// claimNext tries each class once by weighted time, smallest first, and
// returns the first claimable item. For each earlier empty class, it limits
// the worker time that class can use before the claimed class runs again
// to one slice budget. It resets weighted times when every class lacks
// claimable work. ErrNoServingIndex ends the attempt without a claim or
// error. Any other claim error ends the attempt with that error.
func (w *SearchWorker) claimNext(ctx context.Context) (searchdomain.Work, bool, error) {
	var none searchdomain.Work
	order := w.shares.order()
	for tried, position := range order {
		class := scheduledClasses[position]
		work, claimed, err := w.ports.Store.Claim(ctx, class, w.owner, w.settings.Lease)
		switch {
		case err == nil && claimed:
			for _, empty := range order[:tried] {
				w.shares.limitBank(empty, position)
			}
			return work, true, nil
		case err == nil:
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
	w.shares.reset()
	return none, false, nil
}

type loggedWorkerError struct{ err error }

func (e loggedWorkerError) Error() string { return e.err.Error() }
func (e loggedWorkerError) Unwrap() error { return e.err }
