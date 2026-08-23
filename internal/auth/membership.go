package auth

import (
	"context"

	"github.com/google/uuid"
)

type membershipKey struct{}

// WithOrgMembership stores the orgs the authenticated user belongs to on the
// request context. The MCP handler attaches it per request, never at server
// build time, because the built server is cached per user across requests.
func WithOrgMembership(ctx context.Context, orgIDs []uuid.UUID) context.Context {
	copied := make([]uuid.UUID, len(orgIDs))
	copy(copied, orgIDs)
	return context.WithValue(ctx, membershipKey{}, copied)
}

// OrgMembership returns the orgs the request attached for the authenticated
// user. The second result is false when no membership set was attached.
func OrgMembership(ctx context.Context) ([]uuid.UUID, bool) {
	orgIDs, ok := ctx.Value(membershipKey{}).([]uuid.UUID)
	return orgIDs, ok
}
