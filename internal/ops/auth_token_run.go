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
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// authTokenPool opens the auth database. Token commands are SQL only, so they
// run wherever DATABASE_URL resolves, including the host-networked ops
// container that cannot reach FoundationDB.
func authTokenPool(ctx context.Context, factory *cli.Factory) (*pgxpool.Pool, error) {
	pool, err := postgres.NewPool(ctx, factory.Cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.pool_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the auth database for the token command: %w", err)
	}
	return pool, nil
}

func writeAuthTokenResult(ctx context.Context, sink clispec.ResultSink, result clispec.Result) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "auth.token.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the token command report: %w", err)
	}
	return nil
}

func authTokenPrincipal(ctx context.Context, factory *cli.Factory) (audit.OperatorPrincipal, error) {
	principal, err := factory.OperatorIdentitySource().Resolve(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.principal_failed", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, fmt.Errorf("resolve the operator for the token command: %w", err)
	}
	return principal, nil
}

func runAuthTokenCreate(ctx context.Context, factory *cli.Factory, input authTokenCreateInput, sink clispec.ResultSink, execute bool) error {
	pool, err := authTokenPool(ctx, factory)
	if err != nil {
		return err
	}
	defer pool.Close()
	result := authTokenCreateResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.auth.token-create", DryRun: !execute,
		UserID: "", Email: "", Label: strings.TrimSpace(input.Label), OrgID: "", TokenID: "", CreatedAt: "", Token: "",
	}
	if !execute {
		holder, err := authTokenHolder(ctx, pool, input.Email)
		if err != nil {
			return err
		}
		orgID, err := authTokenOrg(ctx, postgres.NewOrgMemberRepo(pool), holder.ID)
		if err != nil {
			return err
		}
		result.UserID, result.Email, result.OrgID = holder.ID.String(), holder.Email, orgID.String()
		return writeAuthTokenResult(ctx, sink, result)
	}
	principal, err := authTokenPrincipal(ctx, factory)
	if err != nil {
		return err
	}
	issue, err := IssueAuthToken(ctx, pool, factory.AuditOutbox(), principal, input.Email, input.Label, clock.Now().UTC())
	if err != nil {
		return err
	}
	result.UserID, result.Email, result.OrgID = issue.Holder.ID.String(), issue.Holder.Email, issue.OrgID.String()
	result.TokenID, result.CreatedAt = issue.Token.ID.String(), issue.Token.CreatedAt.UTC().Format(time.RFC3339)
	result.Label, result.Token = issue.Token.Label, issue.Raw
	if writeErr := writeAuthTokenResult(ctx, sink, result); writeErr != nil {
		// The raw value is shown once and never stored, so a report that
		// failed to reach the operator leaves a live credential nobody
		// holds. Withdraw it through a recorded revocation and say so.
		return withdrawUnconfirmedToken(ctx, pool, factory.AuditOutbox(), principal, issue.Token.ID,
			fmt.Errorf("the token value could not be reported: %w", writeErr))
	}
	return nil
}

func runAuthTokenList(ctx context.Context, factory *cli.Factory, input authTokenListInput, sink clispec.ResultSink) error {
	pool, err := authTokenPool(ctx, factory)
	if err != nil {
		return err
	}
	defer pool.Close()
	holder, err := authTokenHolder(ctx, pool, input.Email)
	if err != nil {
		return err
	}
	tokens, err := postgres.NewTokenRepo(pool).List(ctx, holder.ID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.list_failed",
			slog.String("user_id", holder.ID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("list tokens for %s: %w", holder.Email, err)
	}
	entries := make([]authTokenListEntry, 0, len(tokens))
	for _, stored := range tokens {
		entry := authTokenListEntry{
			TokenID: stored.ID.String(), Label: stored.Label,
			CreatedAt: stored.CreatedAt.UTC().Format(time.RFC3339), LastUsed: "", ExpiresAt: "",
		}
		if stored.LastUsed != nil {
			entry.LastUsed = stored.LastUsed.UTC().Format(time.RFC3339)
		}
		if stored.ExpiresAt != nil {
			entry.ExpiresAt = stored.ExpiresAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}
	return writeAuthTokenResult(ctx, sink, authTokenListResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.auth.token-list",
		UserID: holder.ID.String(), Email: holder.Email, Tokens: entries,
	})
}

func runAuthTokenRevoke(ctx context.Context, factory *cli.Factory, input authTokenRevokeInput, sink clispec.ResultSink, execute bool) error {
	// The value is not echoed anywhere: an operator who pastes the raw bearer
	// where the id belongs would otherwise put it in a log line and, through
	// the choke-point's error row, in the ledger.
	tokenID, err := uuid.Parse(input.TokenID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.id_invalid", slog.String("err", "--id is not a token id"))
		return errors.New("--id is not a token id; pass the id from token-list or token-create, never the bearer value")
	}
	pool, err := authTokenPool(ctx, factory)
	if err != nil {
		return err
	}
	defer pool.Close()
	var revocation AuthTokenRevocation
	if execute {
		principal, err := authTokenPrincipal(ctx, factory)
		if err != nil {
			return err
		}
		revocation, err = RevokeAuthToken(ctx, pool, factory.AuditOutbox(), principal, tokenID, clock.Now().UTC())
		if err != nil {
			return err
		}
	} else {
		revocation, err = lookupAuthToken(ctx, pool, tokenID)
		if err != nil {
			return err
		}
	}
	return writeAuthTokenResult(ctx, sink, authTokenRevokeResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.auth.token-revoke", DryRun: !execute,
		TokenID: revocation.Token.ID.String(), UserID: revocation.Holder.ID.String(),
		Email: revocation.Holder.Email, Label: revocation.Token.Label, OrgID: revocation.OrgID.String(),
	})
}
