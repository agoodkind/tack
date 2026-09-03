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
// The record is not optional. A credential that exists with no ledger row is
// the state the audit epic forbids, so when the record cannot be written the
// token row is deleted again and the issue fails: the operator gets an error
// rather than a usable token nobody can account for.
func IssueAuthToken(
	ctx context.Context,
	pool *pgxpool.Pool,
	outbox audit.OutboxWriter,
	principal audit.OperatorPrincipal,
	email string,
	label string,
	occurredAt time.Time,
) (AuthTokenIssue, error) {
	if strings.TrimSpace(label) == "" {
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
	tokens := postgres.NewTokenRepo(pool)
	issued, err := tokens.Create(ctx, holder.ID, raw, strings.TrimSpace(label))
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.create_failed",
			slog.String("user_id", holder.ID.String()), slog.String("err", err.Error()))
		return AuthTokenIssue{}, fmt.Errorf("issue token for %s: %w", holder.Email, err)
	}
	event := authTokenEvent(audit.VerbAuthTokenCreate, authIssueKeyPrefix, principal, issued, holder, orgID, occurredAt)
	if recordErr := recordAuthTokenEvent(ctx, outbox, event); recordErr != nil {
		if deleteErr := tokens.Delete(ctx, issued.ID); deleteErr != nil {
			slog.ErrorContext(ctx, "auth.token.unrecorded_token_remains",
				slog.String("token_id", issued.ID.String()), slog.String("err", deleteErr.Error()))
			return AuthTokenIssue{}, fmt.Errorf("token %s was issued, could not be recorded, and could not be withdrawn; revoke it by id: %w",
				issued.ID, errors.Join(recordErr, deleteErr))
		}
		return AuthTokenIssue{}, fmt.Errorf("token issue withdrawn because it could not be recorded: %w", recordErr)
	}
	slog.InfoContext(ctx, "auth.token.issued",
		slog.String("token_id", issued.ID.String()),
		slog.String("user_id", holder.ID.String()),
		slog.String("label", issued.Label))
	return AuthTokenIssue{Token: issued, Holder: holder, OrgID: orgID, Raw: raw}, nil
}

// RevokeAuthToken deletes one token and records the revocation. The row is
// gone from the moment the delete commits, so a record failure afterwards is
// reported with the token's identity rather than hidden: the revocation
// happened and the ledger owes a row for it.
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
	if err := postgres.NewTokenRepo(pool).Delete(ctx, tokenID); err != nil {
		slog.ErrorContext(ctx, "auth.token.revoke_failed",
			slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
		return AuthTokenRevocation{}, fmt.Errorf("revoke token %s: %w", tokenID, err)
	}
	event := authTokenEvent(audit.VerbAuthTokenRevoke, authRevokeKeyPrefix, principal,
		revocation.Token, revocation.Holder, revocation.OrgID, occurredAt)
	if recordErr := recordAuthTokenEvent(ctx, outbox, event); recordErr != nil {
		slog.ErrorContext(ctx, "auth.token.revocation_unrecorded",
			slog.String("token_id", tokenID.String()),
			slog.String("user_id", revocation.Holder.ID.String()),
			slog.String("label", revocation.Token.Label),
			slog.String("err", recordErr.Error()))
		return revocation, fmt.Errorf("token %s was revoked but the revocation could not be recorded: %w", tokenID, recordErr)
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
