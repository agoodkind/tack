package integration_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

// TestRuntimeAuditMigrateOutputFailure runs the built server binary with
// `migrate --execute --to 10` against a database at the outbox migration.
// The process standard output is a read-only file, and the result write
// fails after the migrations commit. The test requires the database at
// version 10, a completed migration step, and a partially_completed command
// result that lists the committed step.
func TestRuntimeAuditMigrateOutputFailure(t *testing.T) {
	dsn := emptyLedgerDatabase(t)
	migrateLedgerTo(t, dsn, migrateOutboxVersion)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("opening the test database pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	binary := buildMigrateServer(t)
	unwritable, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s for reading failed: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = unwritable.Close() })
	operator := uuid.Must(uuid.NewV7())
	logDir := t.TempDir()
	process := exec.CommandContext(context.WithoutCancel(t.Context()), binary,
		"--operator-id", operator.String(), "--operator-email", outcomeOperatorEmail,
		"migrate", "--execute", "--to", strconv.Itoa(migrateOutcomeTarget))
	process.Env = append(os.Environ(), "DATABASE_URL="+dsn, "AUDIT_OPERATOR_DSN="+dsn, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
	var stderr bytes.Buffer
	process.Stdout, process.Stderr = unwritable, &stderr
	if err := process.Run(); err == nil {
		t.Fatalf("migrate succeeded although its standard output was read-only:\n%s", stderr.String())
	}
	status, err := schemastatus.Read(t.Context(), dsn, migrations.FS)
	if err != nil || status.AppliedVersion != migrateOutcomeTarget {
		t.Fatalf("the database is at version %d (read error %v) instead of %d", status.AppliedVersion, err, migrateOutcomeTarget)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbDatabaseMigrate), operator.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("migrate recorded %d intents instead of 1", len(intents))
	}
	operationID := intents[0].Context.OperationID
	steps := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(steps) != 1 || steps[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("migrate recorded %d step results; the test requires one completed step result", len(steps))
	}
	results := eventsInPhase(events, audit.PhaseResult, uuid.Nil)
	if len(results) != 1 || results[0].Context.OperationID != operationID {
		t.Fatalf("migrate recorded %d command results; the test requires one result for operation %s", len(results), operationID)
	}
	result := results[0]
	if result.Outcome != audit.OutcomePartiallyCompleted || len(result.Context.CommittedSteps) != 1 || result.Error == nil {
		t.Fatalf("the command result is %s with committed steps %v and error %+v instead of partially_completed with one committed step",
			result.Outcome, result.Context.CommittedSteps, result.Error)
	}
}
