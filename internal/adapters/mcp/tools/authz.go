package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
)

// authorization is the per-call record that a membership check passed. The
// tool wrapper installs one before the handler runs and refuses any successful
// result whose handler never set it, so a handler that skips every membership
// check cannot return data.
type authorization struct {
	granted bool
}

type authorizationKey struct{}

func withAuthorization(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationKey{}, &authorization{granted: false})
}

// markAuthorized records that the caller's membership in the org behind the
// data this call returns has been checked.
func markAuthorized(ctx context.Context) {
	state, ok := ctx.Value(authorizationKey{}).(*authorization)
	if ok {
		state.granted = true
	}
}

func isAuthorized(ctx context.Context) bool {
	state, ok := ctx.Value(authorizationKey{}).(*authorization)
	return ok && state.granted
}

// callerOrgIDs returns the orgs the caller belongs to: the membership set the
// request attached, or a fresh lookup when a caller outside the HTTP handler
// attached none.
func (r *Resolver) callerOrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	if orgIDs, ok := auth.OrgMembership(ctx); ok {
		return orgIDs, nil
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated")
	}
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "authz.membership_lookup_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("list orgs for user %s: %w", userID, err)
	}
	return orgIDs, nil
}

// requireMembership refuses with not-found unless the caller belongs to orgID,
// and marks the call authorized when the caller does. Not-found rather than
// permission denied, so a caller cannot tell a foreign node from a missing one.
func (r *Resolver) requireMembership(ctx context.Context, orgID uuid.UUID, kind, input string) error {
	orgIDs, err := r.callerOrgIDs(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(orgIDs, orgID) {
		markAuthorized(ctx)
		return nil
	}
	slog.WarnContext(ctx, "authz.membership_refused",
		slog.String("kind", kind), slog.String("input", input), slog.String("org_id", orgID.String()))
	return fmt.Errorf("%s %q: %w", kind, input, domain.ErrNotFound)
}
