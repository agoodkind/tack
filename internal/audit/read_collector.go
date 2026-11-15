package audit

import (
	"context"
	"sync"
)

type readCollectorKey struct{}

type readCollector struct {
	mu      sync.Mutex
	outcome OutcomeDetails
}

// WithReadCollector creates a slot for the handler's security decision.
func WithReadCollector(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		readCollectorKey{},
		&readCollector{mu: sync.Mutex{}, outcome: NoOutcome()},
	)
}

// HasReadCollector reports whether ctx has a read collector. The request
// recorder attaches one to every request that it records as a read.
func HasReadCollector(ctx context.Context) bool {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	return collector != nil
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

	collector.outcome = OutcomeDetails{
		Outcome:        outcome,
		FailureCode:    code,
		FailureMessage: message,
		CommittedSteps: nil,
	}
}

// ReadDecision returns the first decision the handler recorded.
func ReadDecision(ctx context.Context) OutcomeDetails {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return NoOutcome()
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()

	return collector.outcome
}

// NoOutcome returns empty outcome details.
func NoOutcome() OutcomeDetails {
	return OutcomeDetails{Outcome: "", FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}
