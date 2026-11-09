package auth

import (
	"context"

	"github.com/google/uuid"
)

type membershipKey struct{}

// WithOrgMembership stores the authenticated user's organizations in the
// request context.
//
// The auth middleware or MCP handler attaches memberships for each request,
// rather than when building the MCP server. The MCP handler caches that
// server per user across requests. WithOrgMembership copies orgIDs before
// storing it.
//
// The request context supports a later membership read:
//
//	ctx = WithOrgMembership(ctx, orgIDs)
//	memberships, attached := OrgMembership(ctx)
func WithOrgMembership(ctx context.Context, orgIDs []uuid.UUID) context.Context {
	copied := make([]uuid.UUID, len(orgIDs))
	copy(copied, orgIDs)
	return context.WithValue(ctx, membershipKey{}, copied)
}

// OrgMembership returns the authenticated user's organizations from the
// request context.
//
// The second result is false when the context does not contain a membership
// set. An attached empty set still produces true.
//
// The MCP handler uses the flag to decide whether it must read memberships:
//
//	orgIDs, attached := auth.OrgMembership(ctx)
//	if !attached {
//		orgIDs, err = h.members.ListOrgIDsForUser(ctx, userID)
//	}
func OrgMembership(ctx context.Context) ([]uuid.UUID, bool) {
	orgIDs, ok := ctx.Value(membershipKey{}).([]uuid.UUID)
	return orgIDs, ok
}
