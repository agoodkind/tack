package foundationdb

import (
	"context"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// SearchQueryAccess resolves entry point authority and compiles caller
// access for public search with policies. FoundationDB error 1031 reports a
// transaction timeout. A failure with that error or
// [context.DeadlineExceeded] also wraps [searchdomain.ErrEngineUnavailable].
// Every other failure returns unchanged.
type SearchQueryAccess struct {
	policies *searchaccess.PolicySet
}

// NewSearchQueryAccess creates the caller access reader for public search.
//
// The reader uses policies to resolve entry point authorities and compile
// caller filters.
//
//   - Public access reads classify FoundationDB timeouts as engine
//     unavailability.
func NewSearchQueryAccess(policies *searchaccess.PolicySet) *SearchQueryAccess {
	return &SearchQueryAccess{policies: policies}
}

// EntryAuthority returns the permission authority of one entry point.
//
// The registered policies resolve the authority. A FoundationDB timeout or
// context deadline also matches [searchdomain.ErrEngineUnavailable].
//
//   - Other errors retain their original identity and text.
func (a *SearchQueryAccess) EntryAuthority(
	ctx context.Context,
	entryPointID uuid.UUID,
) (uuid.UUID, error) {
	authority, err := a.policies.EntryAuthority(ctx, entryPointID)
	if err != nil {
		return uuid.Nil, classifySearchTimeout(err)
	}

	return authority, nil
}

// Query returns the caller's access filter for one query.
//
// The policy set compiles the requested policy version and caller keys. A
// FoundationDB timeout or context deadline also matches
// [searchdomain.ErrEngineUnavailable].
//
//   - A successful filter contains sorted unique opaque keys.
func (a *SearchQueryAccess) Query(
	ctx context.Context,
	request searchaccess.AccessRequest,
) (searchdomain.AccessFilter, error) {
	filter, err := a.policies.Query(ctx, request)
	if err != nil {
		return searchdomain.AccessFilter{}, classifySearchTimeout(err)
	}

	return filter, nil
}
