package integration_test

import (
	"context"
	_ "embed"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_sql_rows.sql
var runtimeAuditSQLRowsQuery string

// sqlAuditScaleName is the datagen scale of the SQL audit identities.
const sqlAuditScaleName = "small"

// sqlAuditConfig loads the server configuration for the test ledger and the
// test FoundationDB cluster and sets its DATABASE_URL to dsn.
func sqlAuditConfig(t *testing.T, dsn string) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load error = %v, want nil", err)
	}
	cfg.DatabaseURL = dsn
	return cfg
}

func sqlAuditScale(t *testing.T) datagen.Scale {
	t.Helper()
	scale, err := datagen.ParseScale(sqlAuditScaleName)
	if err != nil {
		t.Fatalf("datagen.ParseScale(%q) error = %v, want nil", sqlAuditScaleName, err)
	}
	return scale
}

// bootstrapSQLAuditIdentities runs datagen.BootstrapIdentities for seed with
// ctx against the ledger at dsn and returns the identities.
func bootstrapSQLAuditIdentities(t *testing.T, ctx context.Context, dsn string, seed int64) datagen.Identities {
	t.Helper()
	identities, err := datagen.BootstrapIdentities(ctx, sqlAuditConfig(t, dsn), seed, sqlAuditScale(t))
	if err != nil {
		t.Fatalf("datagen.BootstrapIdentities for seed %d error = %v, want nil", seed, err)
	}
	return identities
}

// plannedSQLAuditIdentities returns the identities that
// datagen.BootstrapIdentities creates for seed, without writing them.
func plannedSQLAuditIdentities(t *testing.T, seed int64) datagen.Identities {
	t.Helper()
	return datagen.PlanIdentities(sqlAuditConfig(t, testenv.Ledger(t)), seed, sqlAuditScale(t))
}

// createSQLAuditUser runs datagen.BootstrapIdentities for a new seed with an
// operation of principal against the ledger that pool connects to. It returns
// the stored user of the first actor.
func createSQLAuditUser(t *testing.T, pool *pgxpool.Pool, principal audit.OperatorPrincipal) *user.User {
	t.Helper()
	ctx, _ := sqlAuditContext(t, principal)
	identities := bootstrapSQLAuditIdentities(t, ctx, pool.Config().ConnString(), clock.Now().UnixNano())
	holderID := identities.Workspaces[0].Actors[0].UserID
	holder, err := postgres.NewUserRepo(pool).GetByID(t.Context(), holderID)
	if err != nil {
		t.Fatalf("UserRepo.GetByID(%s) error = %v, want nil", holderID, err)
	}
	return holder
}

// actorUserIDs returns the user ID of every actor in identities.
func actorUserIDs(identities datagen.Identities) []uuid.UUID {
	userIDs := make([]uuid.UUID, 0)
	for _, workspace := range identities.Workspaces {
		for _, actor := range workspace.Actors {
			userIDs = append(userIDs, actor.UserID)
		}
	}
	return userIDs
}

// sqlRows returns the sorted users, api_tokens, and org_members rows of
// userIDs as JSON text. The api_tokens rows omit token_hash.
func sqlRows(t *testing.T, pool *pgxpool.Pool, userIDs []uuid.UUID) []string {
	t.Helper()
	ids := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		ids = append(ids, userID.String())
	}
	rows, err := pool.Query(t.Context(), runtimeAuditSQLRowsQuery, ids)
	if err != nil {
		t.Fatalf("query the rows of users %v: got error %v, want nil", userIDs, err)
	}
	defer rows.Close()
	stored := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan a stored row: got error %v, want nil", err)
		}
		stored = append(stored, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the rows of users %v: got error %v, want nil", userIDs, err)
	}
	return stored
}

// requireSQLUnchanged requires the users, api_tokens, and org_members rows of
// userIDs to equal before. It requires the operator outbox to hold zero
// events with operationID as their operation ID.
func requireSQLUnchanged(t *testing.T, pool *pgxpool.Pool, userIDs []uuid.UUID, before []string, operationID uuid.UUID) {
	t.Helper()
	after := sqlRows(t, pool, userIDs)
	if !slices.Equal(after, before) {
		t.Fatalf("stored rows after the failed audit write = %v, want the rows before it %v", after, before)
	}
	if events := sqlOutboxEvents(t, pool, operationID); len(events) != 0 {
		t.Fatalf("outbox events for operation %s = %+v, want none", operationID, events)
	}
}
