package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
)

// AuthTokenRevocation is what one revocation removed.
type AuthTokenRevocation struct {
	Token  *token.Token
	Holder *user.User
	OrgID  uuid.UUID
}

// RevokeAuthToken looks up the token, its holder regardless of active status,
// and its org, then deletes it through TokenRepo.Delete with principal as the
// audit actor.
func RevokeAuthToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	principal audit.OperatorPrincipal,
	tokenID uuid.UUID,
) (AuthTokenRevocation, error) {
	revocation, err := lookupAuthToken(ctx, pool, tokenID)
	if err != nil {
		return AuthTokenRevocation{}, err
	}
	operation := authTokenOperation(ctx, audit.VerbAuthTokenRevoke, principal, revocation.OrgID)
	if err := postgres.NewTokenRepo(pool).Delete(ctx, operation, tokenID); err != nil {
		slog.ErrorContext(ctx, "auth.token.revoke_failed",
			slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
		return AuthTokenRevocation{}, fmt.Errorf("revoke token %s: %w", tokenID, err)
	}
	slog.InfoContext(ctx, "auth.token.revoked",
		slog.String("token_id", tokenID.String()),
		slog.String("user_id", revocation.Holder.ID.String()))
	return revocation, nil
}

// lookupAuthToken resolves a token id to the token, its user, and its org,
// which is everything a revocation and its dry run report. The holder is read
// whether or not the account is active, because a deactivated user's token
// still authenticates and revoking it is the point.
func lookupAuthToken(ctx context.Context, pool *pgxpool.Pool, tokenID uuid.UUID) (AuthTokenRevocation, error) {
	stored, err := postgres.NewTokenRepo(pool).GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return AuthTokenRevocation{}, fmt.Errorf("no token %s exists: %w", tokenID, err)
		}
		slog.ErrorContext(ctx, "auth.token.lookup_failed",
			slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
		return AuthTokenRevocation{}, fmt.Errorf("find token %s: %w", tokenID, err)
	}
	holder, err := postgres.NewUserRepo(pool).GetByIDIncludingInactive(ctx, stored.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.holder_lookup_failed",
			slog.String("user_id", stored.UserID.String()), slog.String("err", err.Error()))
		return AuthTokenRevocation{}, fmt.Errorf("find the user holding token %s: %w", tokenID, err)
	}
	orgID, err := authTokenOrg(ctx, postgres.NewOrgMemberRepo(pool), holder.ID)
	if err != nil {
		return AuthTokenRevocation{}, err
	}
	return AuthTokenRevocation{Token: stored, Holder: holder, OrgID: orgID}, nil
}
