package audit

import (
	"context"
	"sync"
)

type readCollectorKey struct{}

// readCollector stores the returned identities and the first reported outcome of one request.
type readCollector struct {
	mu      sync.Mutex
	targets []Target
	seen    map[Target]struct{}
	outcome OutcomeDetails
}

// WithReadCollector returns a context with an empty collector for the identities
// and outcome of one public read.
func WithReadCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, readCollectorKey{}, &readCollector{
		mu: sync.Mutex{}, targets: nil, seen: map[Target]struct{}{}, outcome: noOutcome(),
	})
}

// AddReadTargets appends each target that the collector in ctx does not already contain.
// It does not add targets when ctx does not have a collector.
func AddReadTargets(ctx context.Context, targets ...Target) {
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

// CollectedRead returns the identities in insertion order and the first reported outcome from the collector in ctx.
// It returns a zero outcome if ctx does not have a collector or the collector has not recorded an outcome.
func CollectedRead(ctx context.Context) ([]Target, OutcomeDetails) {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return nil, noOutcome()
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]Target(nil), collector.targets...), collector.outcome
}

// noOutcome returns outcome details without an outcome.
func noOutcome() OutcomeDetails {
	return OutcomeDetails{Outcome: "", FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}
