package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

// TestRuntimeAuditProviderRefresh runs the migration state read that the Tack
// OpenTofu provider runs for refresh and plan: migrate --output json without
// --execute, with the service identity flags that the provider passes. The
// command connects to the ledger as a login that is a member of
// audit_operator. The test runs the real relay and consumer and checks one
// completed ledger read event with the SHA-256 digest of the printed result
// member in compact form and one returned identity per embedded migration.
func TestRuntimeAuditProviderRefresh(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	ledgerDSN := testenv.Ledger(t)
	operatorDSN := roleLoginDSN(t, stack.admin, ledgerDSN, "audit_operator")
	service := "tack-provider-" + uuid.NewString()[:8]
	started := clock.Now().UTC().Add(-time.Minute)
	stdout := runProviderStatusRead(t, ledgerDSN, operatorDSN, service)
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		t.Fatalf("decoding the status document failed: %v\n%s", err, stdout)
	}
	available, err := schemastatus.Available(t.Context(), migrations.FS)
	if err != nil {
		t.Fatalf("listing the embedded migrations failed: %v", err)
	}
	startOutboxRelay(t, stack)
	row := waitForActorRow(t, stack, string(audit.VerbDatabaseMigrate), cli.ServiceActorID(service), started)
	var payload bytes.Buffer
	if err := json.Compact(&payload, envelope.Result); err != nil {
		t.Fatalf("compacting the printed result failed: %v", err)
	}
	payloadDigest := sha256.Sum256(payload.Bytes())
	manifestDigest, manifestParts, err := audit.ReadManifestDigest(schemastatus.MigrationTargets(available))
	if err != nil {
		t.Fatalf("computing the migration manifest digest failed: %v", err)
	}
	want := audit.ReadEvidence{
		Count: uint64(len(available)), PayloadSHA256: hex.EncodeToString(payloadDigest[:]), PayloadFormat: "application/json",
		ManifestSHA256: manifestDigest, ManifestParts: manifestParts,
	}
	if row.Outcome != audit.OutcomeCompleted || row.Context.OperationClass != audit.OperationRead || row.Context.Read == nil ||
		*row.Context.Read != want {
		t.Fatalf("read is %s %s %+v; evidence must match %+v", row.Outcome, row.Context.OperationClass, row.Context.Read, want)
	}
}

// runProviderStatusRead builds the server and runs the provider status read
// with the provider service identity. It returns stdout.
func runProviderStatusRead(t *testing.T, ledgerDSN, operatorDSN, service string) []byte {
	t.Helper()
	logDir := t.TempDir()
	arguments := []string{
		"migrate", "--output", "json",
		"--operator-service", service, "--operator-session", "session-" + uuid.NewString()[:8],
		"--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", "provider@example.invalid",
	}
	process := exec.CommandContext(context.WithoutCancel(t.Context()), buildMigrateServer(t), arguments...)
	process.Env = append(os.Environ(), "DATABASE_URL="+ledgerDSN, "AUDIT_OPERATOR_DSN="+operatorDSN, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	if err := process.Run(); err != nil {
		t.Fatalf("the provider status read failed: %v\n%s", err, stderr.String())
	}
	return stdout.Bytes()
}

// waitForActorRow returns the ledger row for verb and actorID in the system
// organization after started. It fails the test after runtimeAuditDeadline.
func waitForActorRow(t *testing.T, stack runtimeAuditStack, verb string, actorID uuid.UUID, started time.Time) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		rows, err := stack.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: audit.SystemOrgID(), Oldest: started, Latest: clock.Now().UTC().Add(time.Minute),
			Action: verb, ActorID: actorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 10,
		})
		if err != nil {
			t.Fatalf("the ledger query for %s by %s failed: %v", verb, actorID, err)
		}
		if len(rows) > 1 {
			t.Fatalf("the ledger has %d rows for %s by %s instead of 1", len(rows), verb, actorID)
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if clock.Now().After(deadline) {
			t.Fatalf("the ledger did not return a row for %s by %s within %s", verb, actorID, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
