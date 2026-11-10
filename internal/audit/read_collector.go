package audit

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type readCollectorKey struct{}

// readCollector stores the identities that one request returns and its first
// reported outcome.
type readCollector struct {
	mu        sync.Mutex
	targets   []Target
	byOrg     map[uuid.UUID][]Target
	seen      map[Target]struct{}
	seenInOrg map[orgTarget]struct{}
	outcome   OutcomeDetails
}

type orgTarget struct {
	orgID  uuid.UUID
	target Target
}

// CollectedRead contains the identities that one request returned and its
// first reported outcome. Targets preserves insertion order, and OrgTargets
// groups the identities by organization.
type CollectedRead struct {
	Targets    []Target
	OrgTargets map[uuid.UUID][]Target
	Outcome    OutcomeDetails
}

// WithReadCollector returns a context with an empty collector for one public
// read's identities and outcome.
//
// AddReadTargets and NoteReadOutcome use the returned context. Each invocation
// of WithReadCollector creates a separate collector.
//
//	readContext := WithReadCollector(ctx)
//	collected := ReadCollected(readContext)
func WithReadCollector(ctx context.Context) context.Context {
	return context.WithValue(
		ctx,
		readCollectorKey{},
		&readCollector{
			mu:        sync.Mutex{},
			targets:   nil,
			byOrg:     map[uuid.UUID][]Target{},
			seen:      map[Target]struct{}{},
			seenInOrg: map[orgTarget]struct{}{},
			outcome:   NoOutcome(),
		},
	)
}

// HasReadCollector reports whether ctx contains a read collector.
//
// The request recorder attaches a collector before invoking a read handler.
// A nil collector does not count as an attached collector.
//
//	readContext := WithReadCollector(ctx)
//	present := HasReadCollector(readContext)
func HasReadCollector(ctx context.Context) bool {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	return collector != nil
}

// AddReadTargets appends targets that the collector in ctx has not recorded
// for orgID. It does not add targets when ctx lacks a collector.
//
// The collector preserves insertion order. Its organization groups remove
// duplicates within each organization, and its combined list removes duplicate
// targets across organizations.
//
//	readContext := WithReadCollector(ctx)
//	AddReadTargets(readContext, organizationID, target)
//	collected := ReadCollected(readContext)
func AddReadTargets(ctx context.Context, orgID uuid.UUID, targets ...Target) {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()

	for _, target := range targets {
		key := orgTarget{orgID: orgID, target: target}
		if _, found := collector.seenInOrg[key]; !found {
			collector.seenInOrg[key] = struct{}{}
			collector.byOrg[orgID] = append(collector.byOrg[orgID], target)
		}

		if _, found := collector.seen[target]; !found {
			collector.seen[target] = struct{}{}
			collector.targets = append(collector.targets, target)
		}
	}
}

// NoteReadOutcome stores outcome, code, and message when the collector in ctx
// has not recorded a nonempty outcome.
//
// The function does not record an outcome when ctx lacks a collector.
// A previously recorded nonempty outcome takes precedence over later calls.
//
//	readContext := WithReadCollector(ctx)
//	NoteReadOutcome(readContext, OutcomeFailed, "read_failed", "the read failed")
//	collected := ReadCollected(readContext)
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

// ReadCollected returns the identities and first reported outcome from the
// collector in ctx.
//
// The result contains copies of the target slices and organization map.
// The outcome remains zero when ctx lacks a collector or the collector has
// not recorded an outcome.
//
//	readContext := WithReadCollector(ctx)
//	AddReadTargets(readContext, organizationID, target)
//	collected := ReadCollected(readContext)
func ReadCollected(ctx context.Context) CollectedRead {
	collector, _ := ctx.Value(readCollectorKey{}).(*readCollector)
	if collector == nil {
		return CollectedRead{
			Targets:    nil,
			OrgTargets: map[uuid.UUID][]Target{},
			Outcome:    NoOutcome(),
		}
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()

	byOrg := make(map[uuid.UUID][]Target, len(collector.byOrg))
	for orgID, targets := range collector.byOrg {
		byOrg[orgID] = append([]Target(nil), targets...)
	}

	return CollectedRead{
		Targets:    append([]Target(nil), collector.targets...),
		OrgTargets: byOrg,
		Outcome:    collector.outcome,
	}
}

// NoOutcome returns empty outcome details.
//
// The result leaves Outcome, FailureCode, and FailureMessage empty and sets
// CommittedSteps to nil.
//
//	outcome := NoOutcome()
//	result.Outcome = outcome
func NoOutcome() OutcomeDetails {
	return OutcomeDetails{Outcome: "", FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}
