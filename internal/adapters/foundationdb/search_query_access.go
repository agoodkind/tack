package foundationdb

import (
	"context"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// SearchQueryAccess resolves entry point authority and compiles caller
// access for public search with policies. A failure with FoundationDB error
// 1031 also wraps [searchdomain.ErrEngineUnavailable]. Every other failure
// returns unchanged.
type SearchQueryAccess struct {
	policies *searchaccess.PolicySet
}

// NewSearchQueryAccess creates the caller access reader of public search.
func NewSearchQueryAccess(policies *searchaccess.PolicySet) *SearchQueryAccess {
	return &SearchQueryAccess{policies: policies}
}

// EntryAuthority returns the permission authority of one entry point.
func (a *SearchQueryAccess) EntryAuthority(ctx context.Context, entryPointID uuid.UUID) (uuid.UUID, error) {
	authority, err := a.policies.EntryAuthority(ctx, entryPointID)
	if err != nil {
		return uuid.Nil, classifySearchTimeout(err)
	}
	return authority, nil
}

// Query returns the caller's access filter for one query.
func (a *SearchQueryAccess) Query(ctx context.Context, request searchaccess.AccessRequest) (searchdomain.AccessFilter, error) {
	filter, err := a.policies.Query(ctx, request)
	if err != nil {
		return searchdomain.AccessFilter{}, classifySearchTimeout(err)
	}
	return filter, nil
}
