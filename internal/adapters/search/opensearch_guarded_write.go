package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxGuardedAttempts bounds the reads of one guarded write. Each attempt
// after the first follows a 409 from a write that changed a page between
// its read and its index request.
const maxGuardedAttempts = 3

// errConcurrentWrite reports a 409 on an index request with if_seq_no or a
// create request: another write changed the page after the read.
var errConcurrentWrite = errors.New("another write changed the page after it was read")

// pagePrecondition is the stored state an index request requires. Create
// requires an absent document; otherwise the request requires the sequence
// number and primary term of the read.
type pagePrecondition struct {
	Create      bool
	SeqNo       int64
	PrimaryTerm int64
}

// guardedPage is one page write sent only after a read of the stored page.
type guardedPage struct {
	documentID string
	generation int64
	encode     func(pagePrecondition) ([]byte, error)
}

// staleRule decides the result of a write below the stored generation.
type staleRule uint8

const (
	// staleRefused refuses a content write below the stored generation or to
	// a retired page with ErrObsoleteWrite. A page at the write generation
	// needs no request.
	staleRefused staleRule = iota + 1
	// staleSuperseded accepts a retirement below the stored generation
	// without a request: the newer stored write supersedes it.
	staleSuperseded
)

// guardDecision is the action for one page after its read.
type guardDecision uint8

const (
	guardWrite guardDecision = iota + 1
	guardSkip
	guardStale
)

func decideGuardedWrite(state pageState, generation int64, rule staleRule) guardDecision {
	switch {
	case !state.Found:
		return guardWrite
	case rule == staleSuperseded && state.Generation > generation:
		return guardSkip
	case rule == staleSuperseded:
		return guardWrite
	case state.Retired || state.Generation > generation:
		return guardStale
	case state.Generation == generation:
		return guardSkip
	default:
		return guardWrite
	}
}

// guardedPlan is the requests for one attempt of a guarded write. positions[i]
// is the page position of operation i; positions[len(operations)] is the
// position of the stale page, or the page count when stale is nil.
type guardedPlan struct {
	operations []bulkOperation
	positions  []int
	stale      error
}

// writeGuarded reads the stored state of pages in index, compares each
// stored generation with the write generation, and sends index or create
// requests with the read as their precondition. After a 409 it reads the
// unaccepted pages again, at most maxGuardedAttempts times in total. It
// returns the accepted prefix length and the encoded bytes sent.
func (a *Adapter) writeGuarded(ctx context.Context, index string, pages []guardedPage, rule staleRule) (int, int, error) {
	accepted, written := 0, 0
	for attempt := 1; ; attempt++ {
		remaining := pages[accepted:]
		if len(remaining) == 0 {
			return accepted, written, nil
		}
		ids := make([]string, 0, len(remaining))
		for _, page := range remaining {
			ids = append(ids, page.documentID)
		}
		states, err := a.readPageStates(ctx, index, ids)
		if err != nil {
			return accepted, written, err
		}
		plan, err := planGuardedWrite(ctx, index, remaining, states, rule)
		if err != nil {
			return accepted, written, err
		}
		for _, operation := range plan.operations {
			written += len(operation.encoded)
		}
		count, err := a.submitBulk(ctx, index, plan.operations, bulkGuarded)
		if err != nil {
			accepted += plan.positions[count]
			if errors.Is(err, errConcurrentWrite) && attempt < maxGuardedAttempts {
				continue
			}
			return accepted, written, err
		}
		if plan.stale != nil {
			return accepted + plan.positions[len(plan.operations)], written, plan.stale
		}
		return len(pages), written, nil
	}
}

// planGuardedWrite encodes the requests for pages in order and stops at the
// first stale page.
func planGuardedWrite(ctx context.Context, index string, pages []guardedPage, states map[string]pageState,
	rule staleRule,
) (guardedPlan, error) {
	operations := make([]bulkOperation, 0, len(pages))
	positions := make([]int, 0, len(pages)+1)
	for position, page := range pages {
		state := states[page.documentID]
		switch decideGuardedWrite(state, page.generation, rule) {
		case guardSkip:
			continue
		case guardStale:
			stale := fmt.Errorf("document %s in %s has generation %d, retired %t, at or above write generation %d: %w",
				page.documentID, index, state.Generation, state.Retired, page.generation, searchdomain.ErrObsoleteWrite)
			telemetry.L(ctx).InfoContext(ctx, "search.page.stale_refused", slog.String("err", stale.Error()),
				slog.String("document_id", page.documentID), slog.String("index", index))
			return guardedPlan{operations: operations, positions: append(positions, position), stale: stale}, nil
		case guardWrite:
		}
		precondition := pagePrecondition{Create: !state.Found, SeqNo: state.SeqNo, PrimaryTerm: state.PrimaryTerm}
		encoded, err := page.encode(precondition)
		if err != nil {
			return guardedPlan{operations: nil, positions: nil, stale: nil}, err
		}
		operations = append(operations, bulkOperation{documentID: page.documentID, encoded: encoded})
		positions = append(positions, position)
	}
	return guardedPlan{operations: operations, positions: append(positions, len(pages)), stale: nil}, nil
}
