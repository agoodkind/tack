package foundationdb

import (
	"context"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
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

// trackRegisteredAccess updates state's page access tracking and reports whether state changed.
// It does not change state when tracking is off. It records the first page's access in PageAccess.
// For later pages with matching versions and keys, it records the lowest registered generation.
// It clears PageAccess and turns tracking off when versions or keys differ.
func trackRegisteredAccess(state *searchAccessRecord, registered node.SearchAccess) bool {
	if !state.PageAccessTracked {
		return false
	}
	if state.PageAccess == nil {
		recorded := node.SearchAccess{
			Versions: slices.Clone(registered.Versions), Keys: slices.Clone(registered.Keys), Generation: registered.Generation,
		}
		state.PageAccess = &recorded
		return true
	}
	if !sameAccess(*state.PageAccess, registered) {
		state.PageAccess, state.PageAccessTracked = nil, false
		return true
	}
	if registered.Generation < state.PageAccess.Generation {
		state.PageAccess.Generation = registered.Generation
		return true
	}
	return false
}

// registeredPagesCurrent uses tracking to report whether every issued page stores the compiled
// versions and keys and returns the access generation to record. It returns false and generation
// if tracking is off, a page repair is pending, or registered versions or keys differ.
// If registration has not recorded a page, it returns true and generation. For matching access,
// it returns true and the lowest registered page generation.
func registeredPagesCurrent(state searchAccessRecord, compiled node.SearchAccess, generation int64) (bool, int64) {
	if !state.PageAccessTracked || state.PagesPending {
		return false, generation
	}
	if state.PageAccess == nil {
		return true, generation
	}
	if !sameAccess(*state.PageAccess, compiled) {
		return false, generation
	}
	return true, state.PageAccess.Generation
}
