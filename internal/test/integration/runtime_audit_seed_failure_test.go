package integration_test

import (
	"bytes"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_seed_login.sql
var runtimeAuditSeedLoginTemplate string

//go:embed testdata/runtime_audit_seed_drop_login.sql
var runtimeAuditSeedDropLoginTemplate string

//go:embed testdata/runtime_audit_seed_rows.sql
var runtimeAuditSeedRowsQuery string

// seedFailureRows counts the stored rows of the seed user and the outbox
// change events of the operator.
type seedFailureRows struct {
	StoredRows   int64
	ChangeEvents int64
}

// TestRuntimeAuditSeedFailedUserCreate runs the built `seed` command with
// DATABASE_URL set to a login that can read the migration versions and change
// users, api_tokens, and org_members but cannot execute
// public.ops_outbox_write_encoded. The command gate writes its intent to the
// operator outbox through AUDIT_OPERATOR_DSN, the test ledger address. The
// seed must fail when it creates the seed user. The users, api_tokens, and
// org_members rows of the seed email must stay absent, and the outbox must not
// gain a change event by the operator.
func TestRuntimeAuditSeedFailedUserCreate(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	admin := testenv.LedgerPool(t, ledgerDSN)
	binary := buildMigrateServer(t)
	suffix := uuid.NewString()[:8]
	email := "seed-audit-failure-" + suffix + "@example.test"
	operatorID := uuid.Must(uuid.NewV7())
	before := seedRows(t, admin, email, operatorID)

	logDir := t.TempDir()
	process := exec.CommandContext(t.Context(), binary,
		"--execute", "--operator-id", operatorID.String(), "--operator-email", "operator@example.com",
		"seed", "--allow-reseed")
	process.Env = append(os.Environ(),
		"DATABASE_URL="+templateLoginDSN(t, admin, runtimeAuditSeedLoginTemplate, runtimeAuditSeedDropLoginTemplate),
		"AUDIT_OPERATOR_DSN="+ledgerDSN, "FDB_CLUSTER_FILE="+testenv.FoundationDB(t),
		"SEED_EMAIL="+email, "SEED_NAME=Seed Audit Failure Test", "SEED_ORG_SLUG=seed-audit-failure-"+suffix,
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"),
		"LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
	output := &bytes.Buffer{}
	process.Stdout, process.Stderr = output, output
	runErr := process.Run()
	logged, err := os.ReadFile(filepath.Join(logDir, "tack.json"))
	if err != nil {
		t.Fatalf("read the seed log: %v", err)
	}
	if runErr == nil || !strings.Contains(output.String()+string(logged), sqlAuditWriteFailure) {
		t.Fatalf("seed error = %v, want a failed run that reports %q\n%s", runErr, sqlAuditWriteFailure, output.String())
	}
	if after := seedRows(t, admin, email, operatorID); after != before {
		t.Fatalf("rows after the failed seed = %+v, want the rows before it %+v", after, before)
	}
}

// seedRows counts the users, api_tokens, and org_members rows of the user
// with email and the outbox change events by operatorID.
func seedRows(t *testing.T, admin *pgxpool.Pool, email string, operatorID uuid.UUID) seedFailureRows {
	t.Helper()
	var rows seedFailureRows
	if err := admin.QueryRow(t.Context(), runtimeAuditSeedRowsQuery, email, operatorID.String()).Scan(
		&rows.StoredRows, &rows.ChangeEvents,
	); err != nil {
		t.Fatalf("count the rows of user %s: %v", email, err)
	}
	return rows
}
