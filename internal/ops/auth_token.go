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

// AuthTokenRevocation is what one revocation removed.
type AuthTokenRevocation struct {
	Token  *token.Token
	Holder *user.User
	OrgID  uuid.UUID
}

// authTokenHolder finds the user a token belongs to by email. Only an existing
// user can hold a token; creating users is the seed's job and stays there.
func authTokenHolder(ctx context.Context, pool *pgxpool.Pool, email string) (*user.User, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return nil, errors.New("an email is required to name the token's user")
	}
	holder, err := postgres.NewUserRepo(pool).GetByEmail(ctx, normalized)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "auth.token.user_lookup_failed",
			slog.String("email", normalized), slog.String("err", err.Error()))
		return nil, fmt.Errorf("find user %q: %w", normalized, err)
	}
	if holder == nil || errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("no user %q exists; tokens are issued only to existing users", normalized)
	}
	return holder, nil
}

// IssueAuthToken mints one token for an existing user and records the issue.
//
// The order is intent, write, outcome. The token id is chosen here so the
// intent row can name it before the row exists. If the intent cannot be
// recorded nothing is issued. If the outcome cannot be recorded the token is
// withdrawn and the issue fails, because a usable credential whose issue the
// ledger does not confirm is the state the audit epic forbids; the pending
// row then stands as the record of the attempt.
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
	intended := &token.Token{
		ID: uuid.Must(uuid.NewV7()), UserID: holder.ID, Label: trimmedLabel,
		LastUsed: nil, ExpiresAt: nil, CreatedAt: occurredAt,
	}
	intent := authTokenEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, principal,
		intended, holder, orgID, audit.OutcomePending, occurredAt)
	if err := recordAuthTokenEvent(ctx, outbox, intent); err != nil {
		return AuthTokenIssue{}, fmt.Errorf("nothing issued: %w", err)
	}
	tokens := postgres.NewTokenRepo(pool)
	issued, err := tokens.CreateWithID(ctx, intended.ID, holder.ID, raw, trimmedLabel)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.create_failed",
			slog.String("token_id", intended.ID.String()),
			slog.String("user_id", holder.ID.String()), slog.String("err", err.Error()))
		return AuthTokenIssue{}, fmt.Errorf("issue token %s for %s: %w", intended.ID, holder.Email, err)
	}
	outcome := authTokenEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, principal,
		issued, holder, orgID, audit.OutcomeOK, occurredAt)
	if recordErr := recordAuthTokenEvent(ctx, outbox, outcome); recordErr != nil {
		return AuthTokenIssue{}, withdrawUnrecordedToken(ctx, tokens, issued.ID, recordErr)
	}
	slog.InfoContext(ctx, "auth.token.issued",
		slog.String("token_id", issued.ID.String()),
		slog.String("user_id", holder.ID.String()),
		slog.String("label", issued.Label))
	return AuthTokenIssue{Token: issued, Holder: holder, OrgID: orgID, Raw: raw}, nil
}

// withdrawUnrecordedToken deletes a token whose issue outcome could not be
// recorded and builds the error the operator acts on. When even the delete
// fails, the error names the token id so it can be revoked by hand.
func withdrawUnrecordedToken(ctx context.Context, tokens *postgres.TokenRepo, tokenID uuid.UUID, recordErr error) error {
	if deleteErr := tokens.Delete(ctx, tokenID); deleteErr != nil {
		slog.ErrorContext(ctx, "auth.token.unrecorded_token_remains",
			slog.String("token_id", tokenID.String()), slog.String("err", deleteErr.Error()))
		return fmt.Errorf("token %s was issued, could not be recorded, and could not be withdrawn; revoke it by id: %w",
			tokenID, errors.Join(recordErr, deleteErr))
	}
	slog.WarnContext(ctx, "auth.token.withdrawn", slog.String("token_id", tokenID.String()))
	return fmt.Errorf("token %s withdrawn because its issue could not be recorded: %w", tokenID, recordErr)
}

// RevokeAuthToken deletes one token and records the revocation, intent
// first. The row is gone from the moment the delete commits, so an outcome
// record that fails afterwards is reported with the token's identity rather
// than hidden: the pending row already names what was revoked, and the error
// says the confirming row is owed.
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
	intent := authTokenEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, principal,
		revocation.Token, revocation.Holder, revocation.OrgID, audit.OutcomePending, occurredAt)
	if err := recordAuthTokenEvent(ctx, outbox, intent); err != nil {
		return AuthTokenRevocation{}, fmt.Errorf("nothing revoked: %w", err)
	}
	if err := postgres.NewTokenRepo(pool).Delete(ctx, tokenID); err != nil {
		slog.ErrorContext(ctx, "auth.token.revoke_failed",
			slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
		return AuthTokenRevocation{}, fmt.Errorf("revoke token %s: %w", tokenID, err)
	}
	outcome := authTokenEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, principal,
		revocation.Token, revocation.Holder, revocation.OrgID, audit.OutcomeOK, occurredAt)
	if recordErr := recordAuthTokenEvent(ctx, outbox, outcome); recordErr != nil {
		slog.ErrorContext(ctx, "auth.token.revocation_unconfirmed",
			slog.String("token_id", tokenID.String()),
			slog.String("user_id", revocation.Holder.ID.String()),
			slog.String("label", revocation.Token.Label),
			slog.String("err", recordErr.Error()))
		return revocation, fmt.Errorf("token %s was revoked but the confirming row could not be recorded: %w", tokenID, recordErr)
	}
	slog.InfoContext(ctx, "auth.token.revoked",
		slog.String("token_id", tokenID.String()),
		slog.String("user_id", revocation.Holder.ID.String()))
	return revocation, nil
}

// lookupAuthToken resolves a token id to the token, its user, and its org,
// which is everything a revocation and its dry run report.
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
	holder, err := postgres.NewUserRepo(pool).GetByID(ctx, stored.UserID)
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
