package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
)

// AuthTokenIssue is what one issue produced. Raw is the bearer value and
// exists only in this struct and in the operator's terminal.
type AuthTokenIssue struct {
	Token  *token.Token
	Holder *user.User
	OrgID  uuid.UUID
	Raw    string
}

// authTokenHolder finds the active user a token is issued to, by the email
// exactly as the users table holds it: the column is case-sensitive and the
// seed stores the address as given, so no case folding happens here. Only an
// existing user can hold a token; creating users is the seed's job.
func authTokenHolder(ctx context.Context, pool *pgxpool.Pool, email string) (*user.User, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return nil, errors.New("an email is required to name the token's user")
	}
	holder, err := postgres.NewUserRepo(pool).GetByEmail(ctx, trimmed)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "auth.token.user_lookup_failed",
			slog.String("email", trimmed), slog.String("err", err.Error()))
		return nil, fmt.Errorf("find user %q: %w", trimmed, err)
	}
	if holder == nil || errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("no active user %q exists; tokens are issued only to existing users", trimmed)
	}
	return holder, nil
}

// IssueAuthToken mints one token for an existing user and records the issue.
//
// The order is intent, write, outcome. The token id is chosen here so the
// intent row can name it before the row exists. A refused intent issues
// nothing. A failed write records an error outcome against the intent. A
// refused outcome withdraws the token through a recorded revocation and the
// issue fails: the raw value was never shown, so nobody holds the credential,
// and the ledger keeps the intent, whatever the outcome write left, and the
// revocation that answered it.
func IssueAuthToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	email string,
	label string,
	occurredAt time.Time,
) (AuthTokenIssue, error) {
	trimmedLabel := strings.TrimSpace(label)
	if trimmedLabel == "" {
		return AuthTokenIssue{}, errors.New("a label is required so the token names the client that holds it")
	}
	holder, err := authTokenHolder(ctx, pool, email)
	if err != nil {
		return AuthTokenIssue{}, err
	}
	orgID, err := authTokenOrg(ctx, postgres.NewOrgMemberRepo(pool), holder.ID)
	if err != nil {
		return AuthTokenIssue{}, err
	}
	raw, err := auth.NewBearerToken()
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.generate_failed", slog.String("err", err.Error()))
		return AuthTokenIssue{}, fmt.Errorf("issue token for %s: %w", holder.Email, err)
	}
	attempt := authTokenAttempt{
		ID: uuid.Must(uuid.NewV7()), Principal: principal, Holder: holder, OrgID: orgID,
		Token: &token.Token{
			ID: uuid.Must(uuid.NewV7()), UserID: holder.ID, Label: trimmedLabel,
			LastUsed: nil, ExpiresAt: nil, CreatedAt: occurredAt,
		},
	}
	intent := authTokenEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, attempt, audit.OutcomePending, occurredAt)
	if err := recordAuthTokenEvent(ctx, outbox, intent); err != nil {
		return AuthTokenIssue{}, fmt.Errorf("nothing issued: %w", err)
	}
	issued, err := postgres.NewTokenRepo(pool).CreateWithID(ctx, attempt.Token.ID, holder.ID, raw, trimmedLabel)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.create_failed",
			slog.String("token_id", attempt.Token.ID.String()),
			slog.String("user_id", holder.ID.String()), slog.String("err", err.Error()))
		wrapped := fmt.Errorf("issue token %s for %s: %w", attempt.Token.ID, holder.Email, err)
		failure := authTokenFailureEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, attempt, wrapped, clock.Now())
		if recordErr := recordAuthTokenEvent(ctx, outbox, failure); recordErr != nil {
			return AuthTokenIssue{}, errors.Join(wrapped, recordErr)
		}
		return AuthTokenIssue{}, wrapped
	}
	attempt.Token = issued
	outcome := authTokenEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, attempt, audit.OutcomeOK, clock.Now())
	if recordErr := recordAuthTokenEvent(ctx, outbox, outcome); recordErr != nil {
		return AuthTokenIssue{}, withdrawUnconfirmedToken(ctx, pool, outbox, principal, issued.ID, recordErr)
	}
	slog.InfoContext(ctx, "auth.token.issued",
		slog.String("token_id", issued.ID.String()),
		slog.String("user_id", holder.ID.String()),
		slog.String("label", issued.Label))
	return AuthTokenIssue{Token: issued, Holder: holder, OrgID: orgID, Raw: raw}, nil
}

// withdrawUnconfirmedToken revokes a token whose issue outcome could not be
// recorded, or whose value could not be reported, through the same recorded
// revocation an operator would run, on a context detached from the command's
// cancellation. The outcome write may have committed before its error came
// back, so a bare delete could leave an ok row for a token that is gone; a
// recorded revocation answers that row either way. When the revocation
// itself fails, the error names the token id so it can be revoked by hand.
func withdrawUnconfirmedToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	tokenID uuid.UUID,
	cause error,
) error {
	withdrawCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	if _, revokeErr := RevokeAuthToken(withdrawCtx, pool, outbox, principal, tokenID, clock.Now()); revokeErr != nil {
		slog.ErrorContext(ctx, "auth.token.unconfirmed_token_remains",
			slog.String("token_id", tokenID.String()), slog.String("err", revokeErr.Error()))
		return fmt.Errorf("token %s was issued and could not be withdrawn; revoke it by id: %w",
			tokenID, errors.Join(cause, revokeErr))
	}
	slog.WarnContext(ctx, "auth.token.withdrawn", slog.String("token_id", tokenID.String()))
	return fmt.Errorf("token %s withdrawn: %w", tokenID, cause)
}
