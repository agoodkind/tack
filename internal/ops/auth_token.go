package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/auth"
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

// IssueAuthToken finds an active user and organization, generates a bearer
// value, and creates the token through TokenRepo.CreateWithID with a new
// UUIDv7 ID. The repository reads the audit operation from ctx. IssueAuthToken
// returns the bearer in Raw for the command to display once; Tack never
// stores the bearer.
func IssueAuthToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	email string,
	label string,
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
	tokenID := uuid.Must(uuid.NewV7())
	issued, err := postgres.NewTokenRepo(pool).CreateWithID(ctx, orgID, tokenID, holder.ID, raw, trimmedLabel)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.create_failed",
			slog.String("token_id", tokenID.String()),
			slog.String("user_id", holder.ID.String()), slog.String("err", err.Error()))
		return AuthTokenIssue{}, failIssue(ctx, pool, tokenID,
			fmt.Errorf("issue token %s for %s: %w", tokenID, holder.Email, err))
	}
	slog.InfoContext(ctx, "auth.token.issued",
		slog.String("token_id", issued.ID.String()),
		slog.String("user_id", holder.ID.String()),
		slog.String("label", issued.Label))
	return AuthTokenIssue{Token: issued, Holder: holder, OrgID: orgID, Raw: raw}, nil
}

// failIssue reconciles an insert error that may follow a commit by calling
// RevokeAuthToken for tokenID on a context detached with
// [context.WithoutCancel]. Successful revocation confirms deletion;
// domain.ErrNotFound confirms that the token does not exist. Other errors leave
// its state unknown and direct the operator to token-list.
func failIssue(
	ctx context.Context,
	pool *pgxpool.Pool,
	tokenID uuid.UUID,
	cause error,
) error {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	_, revokeErr := RevokeAuthToken(reconcileCtx, pool, tokenID)
	switch {
	case revokeErr == nil:
		slog.WarnContext(ctx, "auth.token.create_committed_despite_error", slog.String("token_id", tokenID.String()))
		return fmt.Errorf("issue token %s: creation returned an error; cleanup revoked the token: %w", tokenID, cause)
	case errors.Is(revokeErr, domain.ErrNotFound):
		return cause
	default:
		slog.ErrorContext(ctx, "auth.token.create_unverified",
			slog.String("token_id", tokenID.String()), slog.String("err", revokeErr.Error()))
		return fmt.Errorf("issue token %s: revocation failed; check token-list and revoke it by ID if present: %w",
			tokenID, errors.Join(cause, revokeErr))
	}
}

// withdrawUnconfirmedToken revokes tokenID through RevokeAuthToken when the
// command cannot report the bearer value. It uses [context.WithoutCancel] with
// a timeout and includes tokenID in any error from revocation.
func withdrawUnconfirmedToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	tokenID uuid.UUID,
	cause error,
) error {
	withdrawCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	if _, revokeErr := RevokeAuthToken(withdrawCtx, pool, tokenID); revokeErr != nil {
		slog.ErrorContext(ctx, "auth.token.unconfirmed_token_remains",
			slog.String("token_id", tokenID.String()), slog.String("err", revokeErr.Error()))
		return fmt.Errorf("token %s was issued and could not be withdrawn; revoke it by id: %w",
			tokenID, errors.Join(cause, revokeErr))
	}
	slog.WarnContext(ctx, "auth.token.withdrawn", slog.String("token_id", tokenID.String()))
	return fmt.Errorf("token %s withdrawn: %w", tokenID, cause)
}
