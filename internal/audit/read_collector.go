package audit

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type readCollectorKey struct{}

// readCollector stores the returned identities and the first reported outcome of one request.
type readCollector struct {
	mu      sync.Mutex
	targets []Target
	byOrg   map[uuid.UUID][]Target
	seen    map[Target]struct{}
	outcome OutcomeDetails
}

// CollectedRead contains the identities that one request returned, in insertion
// order and grouped by organization, and the first reported outcome.
type CollectedRead struct {
	Targets    []Target
	OrgTargets map[uuid.UUID][]Target
	Outcome    OutcomeDetails
}

// WithReadCollector returns a context with an empty collector for the identities
// and outcome of one public read.
func WithReadCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, readCollectorKey{}, &readCollector{
		mu: sync.Mutex{}, targets: nil, byOrg: map[uuid.UUID][]Target{}, seen: map[Target]struct{}{}, outcome: noOutcome(),
	})
}

// HasReadCollector reports whether ctx has a read collector. The request
// recorder attaches one to every request that it records as a read.
func HasReadCollector(ctx context.Context) bool {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	return collector != nil
}

// AddReadTargets appends each target of orgID that the collector in ctx does not
// already contain. It does not add targets when ctx does not have a collector.
func AddReadTargets(ctx context.Context, orgID uuid.UUID, targets ...Target) {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, target := range targets {
		if _, found := collector.seen[target]; found {
			continue
		}
		collector.seen[target] = struct{}{}
		collector.targets = append(collector.targets, target)
		collector.byOrg[orgID] = append(collector.byOrg[orgID], target)
	}
}

// NoteReadOutcome stores outcome in the collector in ctx if the collector does not already have an outcome.
// It does not record an outcome when ctx does not have a collector.
func NoteReadOutcome(ctx context.Context, outcome Outcome, code, message string) {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.outcome.Outcome != "" {
		return
	}
	collector.outcome = OutcomeDetails{Outcome: outcome, FailureCode: code, FailureMessage: message, CommittedSteps: nil}
}

// ReadCollected returns the identities and the first reported outcome from the collector in ctx.
// It returns a zero outcome if ctx does not have a collector or the collector has not recorded an outcome.
func ReadCollected(ctx context.Context) CollectedRead {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return CollectedRead{Targets: nil, OrgTargets: map[uuid.UUID][]Target{}, Outcome: noOutcome()}
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	byOrg := make(map[uuid.UUID][]Target, len(collector.byOrg))
	for orgID, targets := range collector.byOrg {
		byOrg[orgID] = append([]Target(nil), targets...)
	}
	return CollectedRead{Targets: append([]Target(nil), collector.targets...), OrgTargets: byOrg, Outcome: collector.outcome}
}

// noOutcome returns empty outcome details.
func noOutcome() OutcomeDetails {
	return OutcomeDetails{Outcome: "", FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}
