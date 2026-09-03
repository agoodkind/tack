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

// authTokenHolder finds the user a token belongs to, by the email exactly as
// the users table holds it: the column is case-sensitive and the seed stores
// the address as given, so no case folding happens here. Issuing needs an
// active user; listing reaches a deactivated one too, because that user's
// tokens still authenticate and are the ones to find and revoke. Creating
// users is the seed's job.
func authTokenHolder(ctx context.Context, pool *pgxpool.Pool, email string, includeInactive bool) (*user.User, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return nil, errors.New("an email is required to name the token's user")
	}
	users := postgres.NewUserRepo(pool)
	var holder *user.User
	var err error
	if includeInactive {
		holder, err = users.GetByEmailIncludingInactive(ctx, trimmed)
	} else {
		holder, err = users.GetByEmail(ctx, trimmed)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "auth.token.user_lookup_failed",
			slog.String("email", trimmed), slog.String("err", err.Error()))
		return nil, fmt.Errorf("find user %q: %w", trimmed, err)
	}
	if holder == nil || errors.Is(err, domain.ErrNotFound) {
		if includeInactive {
			return nil, fmt.Errorf("no user %q exists", trimmed)
		}
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
	holder, err := authTokenHolder(ctx, pool, email, false)
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
		return AuthTokenIssue{}, failIssue(ctx, pool, outbox, attempt,
			fmt.Errorf("issue token %s for %s: %w", attempt.Token.ID, holder.Email, err))
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

// failIssue answers an issue intent whose insert reported failure. The insert
// can fail after the database committed it, so the preselected id is looked
// up first: a row that exists is withdrawn through a recorded revocation,
// and either way the issue attempt records an error outcome, because from
// the operator's side the issue did not happen.
func failIssue(
	ctx context.Context,
	pool *pgxpool.Pool,
	outbox audit.OutboxWriter,
	attempt authTokenAttempt,
	cause error,
) error {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	result := cause
	if _, lookupErr := postgres.NewTokenRepo(pool).GetByID(reconcileCtx, attempt.Token.ID); lookupErr == nil {
		slog.WarnContext(ctx, "auth.token.create_committed_despite_error",
			slog.String("token_id", attempt.Token.ID.String()))
		result = withdrawUnconfirmedToken(ctx, pool, outbox, attempt.Principal, attempt.Token.ID, cause)
	}
	failure := authTokenFailureEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, attempt, result, clock.Now())
	if recordErr := recordAuthTokenEvent(ctx, outbox, failure); recordErr != nil {
		return errors.Join(result, recordErr)
	}
	return result
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
	revocation, revokeErr := RevokeAuthToken(withdrawCtx, pool, outbox, principal, tokenID, clock.Now())
	if revokeErr != nil && revocation.Token == nil {
		slog.ErrorContext(ctx, "auth.token.unconfirmed_token_remains",
			slog.String("token_id", tokenID.String()), slog.String("err", revokeErr.Error()))
		return fmt.Errorf("token %s was issued and could not be withdrawn; revoke it by id: %w",
			tokenID, errors.Join(cause, revokeErr))
	}
	if revokeErr != nil {
		// The delete committed and only the confirming row is missing, so a
		// manual revoke would find nothing; say what is actually owed.
		return fmt.Errorf("token %s withdrawn, its revocation intent is recorded and its confirming row is owed: %w",
			tokenID, errors.Join(cause, revokeErr))
	}
	slog.WarnContext(ctx, "auth.token.withdrawn", slog.String("token_id", tokenID.String()))
	return fmt.Errorf("token %s withdrawn: %w", tokenID, cause)
}
