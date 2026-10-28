package foundationdb

import (
	"context"
	"log/slog"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// hasIssuedDocuments reports whether the node has an issued page document,
// using one bounded read inside tr.
func hasIssuedDocuments(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (bool, error) {
	keyRange, err := fdb.PrefixRange(searchIssuedPrefix(orgID, nodeID))
	if err != nil {
		return false, searchReadFailure(ctx, "create issued range of node "+nodeID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return false, searchReadFailure(ctx, "read issued documents of node "+nodeID.String(), err)
	}
	return len(items) > 0, nil
}

// trackRegisteredAccess returns state with page access tracking updated for one registered page.
// It returns state unchanged when tracking is off.
// It records the first page's access in PageAccess.
// It keeps the lowest registered generation while later pages match the recorded versions and keys.
// When versions or keys differ, it clears PageAccess and turns tracking off.
func trackRegisteredAccess(state searchAccessRecord, registered node.SearchAccess) searchAccessRecord {
	if !state.PageAccessTracked {
		return state
	}
	if state.PageAccess == nil {
		recorded := node.SearchAccess{
			Versions: slices.Clone(registered.Versions), Keys: slices.Clone(registered.Keys), Generation: registered.Generation,
		}
		state.PageAccess = &recorded
		return state
	}
	if !sameAccess(*state.PageAccess, registered) {
		state.PageAccess, state.PageAccessTracked = nil, false
		return state
	}
	lowest := *state.PageAccess
	lowest.Generation = min(lowest.Generation, registered.Generation)
	state.PageAccess = &lowest
	return state
}

// samePageTracking reports whether both records match in PageAccessTracked and PageAccess.
// PageAccess matches when both lack it or its versions, keys, and generation match.
func samePageTracking(first, second searchAccessRecord) bool {
	if first.PageAccessTracked != second.PageAccessTracked {
		return false
	}
	if first.PageAccess == nil || second.PageAccess == nil {
		return first.PageAccess == nil && second.PageAccess == nil
	}
	return sameAccess(*first.PageAccess, *second.PageAccess) && first.PageAccess.Generation == second.PageAccess.Generation
}

// registeredPagesState reports how a node's issued pages compare with compiled access
// according to page access tracking.
type registeredPagesState uint8

const (
	// registeredPagesStored indicates that every issued page already stores the compiled access
	// or the node has not registered any pages.
	registeredPagesStored registeredPagesState = iota + 1
	// registeredPagesUntracked indicates that tracking is off or a page repair is pending.
	registeredPagesUntracked
	// registeredPagesDiffer indicates that registered pages store other versions or keys.
	registeredPagesDiffer
)

// String returns the state name for the Debug log.
func (state registeredPagesState) String() string {
	switch state {
	case registeredPagesStored:
		return "stored"
	case registeredPagesUntracked:
		return "untracked"
	case registeredPagesDiffer:
		return "differ"
	default:
		return "unknown"
	}
}

// registeredPagesCheck uses State to report how registered pages compare with compiled access.
// Generation is the lowest registered page generation when State is registeredPagesStored
// and registered pages exist; otherwise, it is the work generation.
type registeredPagesCheck struct {
	State      registeredPagesState
	Generation int64
}

// checkRegisteredPages compares registered pages with compiled and returns their state.
// For registeredPagesStored with registered pages, it returns their lowest generation.
// Otherwise, it returns generation.
func checkRegisteredPages(state searchAccessRecord, compiled node.SearchAccess, generation int64) registeredPagesCheck {
	switch {
	case !state.PageAccessTracked || state.PagesPending:
		return registeredPagesCheck{State: registeredPagesUntracked, Generation: generation}
	case state.PageAccess == nil:
		return registeredPagesCheck{State: registeredPagesStored, Generation: generation}
	case !sameAccess(*state.PageAccess, compiled):
		return registeredPagesCheck{State: registeredPagesDiffer, Generation: generation}
	default:
		return registeredPagesCheck{State: registeredPagesStored, Generation: state.PageAccess.Generation}
	}
}

// recordCompiledAccess calls checkRegisteredPages and logs search.access.pages_checked at Debug
// with the node ID, state, and generation.
// It calls skipAccessPages for registeredPagesStored and rewriteAccessPages for either other state.
func recordCompiledAccess(
	ctx context.Context, tr fdb.Transaction, work searchdomain.Work, state searchAccessRecord, compiled node.SearchAccess,
) (searchdomain.Work, error) {
	check := checkRegisteredPages(state, compiled, work.Generation)
	telemetry.L(ctx).DebugContext(ctx, "search.access.pages_checked", slog.String("node_id", work.NodeID.String()),
		slog.String("pages", check.State.String()), slog.Int64("generation", check.Generation))
	if check.State == registeredPagesStored {
		return skipAccessPages(ctx, tr, work, compiled, check.Generation)
	}
	return rewriteAccessPages(ctx, tr, work, compiled)
}

// skipAccessPages records compiled at generation with only dependents pending.
// It ends page access tracking and returns work in the dependents phase.
func skipAccessPages(
	ctx context.Context, tr fdb.Transaction, work searchdomain.Work, compiled node.SearchAccess, generation int64,
) (searchdomain.Work, error) {
	access := compiled
	access.Generation = generation
	state := searchAccessRecord{Access: access, PagesPending: false, DependentsPending: true, PageAccess: nil, PageAccessTracked: false}
	if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
		return work, err
	}
	next := work
	next.Phase, next.Cursor = searchdomain.PhaseDependents, ""
	return next, nil
}

// rewriteAccessPages records compiled at the work generation with pages and dependents pending.
// It ends page access tracking and returns work in the pages phase at the first issued document.
func rewriteAccessPages(
	ctx context.Context, tr fdb.Transaction, work searchdomain.Work, compiled node.SearchAccess,
) (searchdomain.Work, error) {
	access := compiled
	access.Generation = work.Generation
	state := searchAccessRecord{Access: access, PagesPending: true, DependentsPending: true, PageAccess: nil, PageAccessTracked: false}
	if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
		return work, err
	}
	next := work
	next.Phase, next.Cursor = searchdomain.PhasePages, ""
	return next, nil
}
