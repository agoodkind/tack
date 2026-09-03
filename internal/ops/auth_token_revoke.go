package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
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

// RevokeAuthToken deletes one token and records the revocation, intent
// first. A failed delete records an error outcome against the intent, so no
// pending row is left claiming a revocation that did not happen. The row is
// gone from the moment the delete commits, so an outcome record that fails
// afterwards is reported with the token's identity rather than hidden: the
// pending row already names what was revoked, and the error says the
// confirming row is owed. A rerun records a new attempt.
func RevokeAuthToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	tokenID uuid.UUID,
	occurredAt time.Time,
) (AuthTokenRevocation, error) {
	revocation, err := lookupAuthToken(ctx, pool, tokenID)
	if err != nil {
		return AuthTokenRevocation{}, err
	}
	attempt := authTokenAttempt{
		ID: uuid.Must(uuid.NewV7()), Principal: principal,
		Token: revocation.Token, Holder: revocation.Holder, OrgID: revocation.OrgID,
	}
	intent := authTokenEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, attempt, audit.OutcomePending, occurredAt)
	if err := recordAuthTokenEvent(ctx, outbox, intent); err != nil {
		return AuthTokenRevocation{}, fmt.Errorf("nothing revoked: %w", err)
	}
	if err := postgres.NewTokenRepo(pool).Delete(ctx, tokenID); err != nil {
		slog.ErrorContext(ctx, "auth.token.revoke_failed",
			slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
		wrapped := fmt.Errorf("revoke token %s: %w", tokenID, err)
		failure := authTokenFailureEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, attempt, wrapped, clock.Now())
		if recordErr := recordAuthTokenEvent(ctx, outbox, failure); recordErr != nil {
			return AuthTokenRevocation{}, errors.Join(wrapped, recordErr)
		}
		return AuthTokenRevocation{}, wrapped
	}
	outcome := authTokenEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, attempt, audit.OutcomeOK, clock.Now())
	if recordErr := recordAuthTokenEvent(ctx, outbox, outcome); recordErr != nil {
		slog.ErrorContext(ctx, "auth.token.revocation_unconfirmed",
			slog.String("token_id", tokenID.String()),
			slog.String("user_id", revocation.Holder.ID.String()),
			slog.String("err", recordErr.Error()))
		return revocation, fmt.Errorf("token %s was revoked but the confirming row could not be recorded: %w", tokenID, recordErr)
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
			return AuthTokenRevocation{}, fmt.Errorf("no token %s exists", tokenID)
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
