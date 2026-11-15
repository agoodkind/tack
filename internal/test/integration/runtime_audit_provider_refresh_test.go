package integration_test

import (
	"bytes"
	"context"
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
	"goodkind.io/tack/internal/testenv"
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

	startOutboxRelay(t, stack)
	row := waitForActorRow(
		t,
		stack,
		string(audit.VerbDatabaseMigrate),
		cli.ServiceActorID(service),
		started,
	)
	if row.Outcome != audit.OutcomeAttempted || row.Context.OperationClass != audit.OperationRead ||
		row.Context.Phase != audit.PhaseAttempt || row.Context.OperationID == uuid.Nil ||
		row.Context.Read != nil || row.Error != nil || !row.Context.CompletedAt.IsZero() {
		t.Fatalf(
			"read attempt is %s %s %+v",
			row.Outcome,
			row.Context.OperationClass,
			row.Context.Read,
		)
	}
}

func runProviderStatusRead(t *testing.T, ledgerDSN, operatorDSN, service string) []byte {
	t.Helper()
	logDir := t.TempDir()
	arguments := []string{
		"migrate",
		"--output",
		"json",
		"--operator-service",
		service,
		"--operator-session",
		"session-" + uuid.NewString()[:8],
		"--operator-id",
		uuid.Must(uuid.NewV7()).String(),
		"--operator-email",
		"provider@example.invalid",
	}
	process := exec.CommandContext(
		context.WithoutCancel(t.Context()),
		buildMigrateServer(t),
		arguments...,
	)
	process.Env = append(
		os.Environ(),
		"DATABASE_URL="+ledgerDSN,
		"AUDIT_OPERATOR_DSN="+operatorDSN,
		"LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"),
		"LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"),
	)
	var stdout, stderr bytes.Buffer
	process.Stdout, process.Stderr = &stdout, &stderr
	if err := process.Run(); err != nil {
		t.Fatalf("the provider status read failed: %v\n%s", err, stderr.String())
	}

	return stdout.Bytes()
}

func waitForActorRow(
	t *testing.T,
	stack runtimeAuditStack,
	verb string,
	actorID uuid.UUID,
	started time.Time,
) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)

	for {
		rows, err := stack.reader.Query(
			t.Context(),
			audit.QueryFilter{
				OrgID:     audit.SystemOrgID(),
				Oldest:    started,
				Latest:    clock.Now().UTC().Add(time.Minute),
				Action:    verb,
				ActorID:   actorID,
				EntityID:  uuid.Nil,
				RequestID: "",
				TraceID:   "",
				Limit:     10,
			},
		)
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
			t.Fatalf(
				"the ledger did not return a row for %s by %s within %s",
				verb,
				actorID,
				runtimeAuditDeadline,
			)
		}

		time.Sleep(200 * time.Millisecond)
	}
}
