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

// authorization records a successful membership check for one call. The tool
// wrapper creates the record before running the handler and refuses a
// successful result when the handler did not mark authorization.
type authorization struct {
	granted bool
}

type authorizationKey struct{}

func withAuthorization(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationKey{}, &authorization{granted: false})
}

// markAuthorized records a successful membership check for the organization
// of the data that the call returns.
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

// callerOrgIDs returns the membership set from the request context. It
// queries the membership repository when the context does not contain that
// set.
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
		slog.ErrorContext(
			ctx,
			"authz.membership_lookup_failed",
			slog.String("user_id", userID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("list orgs for user %s: %w", userID, err)
	}

	return orgIDs, nil
}

// requireMembership returns not-found unless the caller belongs to orgID. A
// successful check marks the call authorized. The public error does not
// distinguish a foreign node from an absent node.
func (r *Resolver) requireMembership(
	ctx context.Context,
	orgID uuid.UUID,
	kind string,
	input string,
) error {
	orgIDs, err := r.callerOrgIDs(ctx)
	if err != nil {
		return err
	}

	if slices.Contains(orgIDs, orgID) {
		markAuthorized(ctx)
		return nil
	}

	slog.WarnContext(
		ctx,
		"authz.membership_refused",
		slog.String("kind", kind),
		slog.String("input", input),
		slog.String("org_id", orgID.String()),
	)

	return membershipRefusalError{err: fmt.Errorf("%s %q: %w", kind, input, domain.ErrNotFound)}
}
