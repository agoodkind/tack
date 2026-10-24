package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

const (
	outcomeOperatorEmail = "outcomes@example.invalid"
	migrateOutcomeTarget = 10
)

type migrationRangeEvidence struct {
	FromVersion int64                    `json:"from_version"`
	ToVersion   int64                    `json:"to_version"`
	Migrations  []schemastatus.Migration `json:"migrations"`
}

// TestRuntimeAuditCommandOutcomes checks command outcomes and evidence in the
// audit outbox:
//   - buckets_init_refused checks that buckets-init refuses to run without
//     object-store settings and records the refusal without a step.
//   - migrate checks refusal without a step when the version guard fails. A
//     successful run records the version range, planned migration file digests,
//     and applied version.
//   - search_reindex checks that the reindex dry run records its read and that
//     reindex records the source index, replacement ID, and generation when it
//     starts a replacement. The replacement record must store the operation of
//     that run. A second run must be refused without a step.
//   - seed_roles checks each role's state before and after seeding and verifies
//     that events do not contain role passwords.
//   - archive_node_refused checks that yb-archive-node refuses to run without
//     TACK_BACKUP_YB_ROCKSDB_DIR and records the refusal without a step.
//   - archive_node_runs checks the manifest evidence of an archive step and
//     the already_archived state of a node prefix that contains every
//     artifact.
//   - staleness_check checks that an empty backup bucket produces a stale
//     verdict while the step and command record completed outcomes. It checks
//     the bucket, the skipped FoundationDB check, and a STALE verdict for each
//     reading in the step evidence. Each reading must appear in the stale list.
//   - access_rollout checks that the access-rollout dry run records its read and
//     that access-rollout records the authority, candidate, backfill phase, and
//     nonzero generation when it starts a rollout. The rollout record must store
//     the operation of that run. A second run must be refused without a step.
//     After access-rollback, the rollout record must store the rollback
//     operation.
func TestRuntimeAuditCommandOutcomes(t *testing.T) {
	t.Run("buckets_init_refused", func(t *testing.T) {
		pool := ledgerAdminPool(t)
		operatorID := uuid.Must(uuid.NewV7())
		_, err := runOpsCommand(t, &config.Config{}, pool, "--execute", "--operator-id", operatorID.String(),
			"--operator-email", outcomeOperatorEmail, "ops", "backup", "buckets-init")
		if err == nil {
			t.Fatal("buckets-init succeeded without object-store settings instead of refusing to run")
		}
		events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsBackupBucketsInit), operatorID.String())
		requireOnlyResult(t, events, audit.OutcomeRefused, "configuration_incomplete")
	})
	t.Run("migrate", testMigrateOutcomes)
	t.Run("search_reindex", testSearchReindexOutcomes)
	t.Run("seed_roles", testSeedRolesOutcomes)
	t.Run("archive_node_refused", testArchiveNodeRefused)
	t.Run("archive_node_runs", testArchiveNodeRuns)
	t.Run("staleness_check", testStalenessCheckOutcome)
	t.Run("access_rollout", testAccessRolloutOutcomes)
}

func testMigrateOutcomes(t *testing.T) {
	dsn := emptyLedgerDatabase(t)
	migrateLedgerTo(t, dsn, migrateOutboxVersion)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("opening the test database pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	binary := buildMigrateServer(t)
	refusedOperator := uuid.Must(uuid.NewV7())
	if output, err := runRecordedMigrate(t, binary, dsn, refusedOperator, "--expect-from", strconv.Itoa(migrateOutboxVersion-1)); err == nil {
		t.Fatalf("migrate succeeded with an incorrect --expect-from value:\n%s", output)
	}
	refused := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbDatabaseMigrate), refusedOperator.String())
	requireOnlyResult(t, refused, audit.OutcomeRefused, "version_guard_refused")

	operator := uuid.Must(uuid.NewV7())
	if output, err := runRecordedMigrate(t, binary, dsn, operator, "--to", strconv.Itoa(migrateOutcomeTarget)); err != nil {
		t.Fatalf("migrate --to %d failed: %v\n%s", migrateOutcomeTarget, err, output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbDatabaseMigrate), operator.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("the outbox contains %d migration intents instead of 1", len(intents))
	}
	operationID := intents[0].Context.OperationID
	stepIntents := eventsInPhase(events, audit.PhaseIntent, operationID)
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(stepIntents) != 1 || len(stepResults) != 1 || stepResults[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("migration steps have %d intents and %d results instead of 1 intent and 1 completed result",
			len(stepIntents), len(stepResults))
	}
	requireMigrationRange(t, stepIntents[0])
	var applied struct {
		AppliedVersion int64 `json:"applied_version"`
	}
	if err := json.Unmarshal(stepResults[0].Extra, &applied); err != nil || applied.AppliedVersion != migrateOutcomeTarget {
		t.Fatalf("migration step result evidence is %s (decode error %v); the applied version should be %d",
			stepResults[0].Extra, err, migrateOutcomeTarget)
	}
	requireCompletedResult(t, events, operationID)
}

// requireMigrationRange requires the step intent to record the range from
// version 5 through migrateOutcomeTarget with the embedded file digests.
func requireMigrationRange(t *testing.T, stepIntent audit.Event) {
	t.Helper()
	var evidence migrationRangeEvidence
	if err := json.Unmarshal(stepIntent.Extra, &evidence); err != nil {
		t.Fatalf("decoding the migration range evidence failed: %v", err)
	}
	available, err := schemastatus.Available(t.Context(), migrations.FS)
	if err != nil {
		t.Fatalf("listing the embedded migrations failed: %v", err)
	}
	var want []schemastatus.Migration
	for _, migration := range available {
		if migration.Version > migrateOutboxVersion && migration.Version <= migrateOutcomeTarget {
			want = append(want, migration)
		}
	}
	if evidence.FromVersion != migrateOutboxVersion || evidence.ToVersion != migrateOutcomeTarget || len(evidence.Migrations) != len(want) {
		t.Fatalf("migration range is %d to %d with %d files; the test requires %d to %d with %d files",
			evidence.FromVersion, evidence.ToVersion, len(evidence.Migrations), migrateOutboxVersion, migrateOutcomeTarget, len(want))
	}
	for index, migration := range want {
		if evidence.Migrations[index] != migration {
			t.Fatalf("the recorded migration is %+v; the test requires %+v", evidence.Migrations[index], migration)
		}
	}
}

func requireOnlyResult(t *testing.T, events []audit.Event, outcome audit.Outcome, code string) {
	t.Helper()
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	results := eventsInPhase(events, audit.PhaseResult, uuid.Nil)
	if len(events) != 2 || len(intents) != 1 || len(results) != 1 {
		t.Fatalf("the outbox has %d events (%d intents, %d results) instead of one intent and one result",
			len(events), len(intents), len(results))
	}
	result := results[0]
	if result.Outcome != outcome || result.Error == nil || result.Error.Code != code {
		t.Fatalf("the result is %s %+v; the test requires %s with code %s", result.Outcome, result.Error, outcome, code)
	}
}

func requireCompletedResult(t *testing.T, events []audit.Event, operationID uuid.UUID) {
	t.Helper()
	results := eventsInPhase(events, audit.PhaseResult, uuid.Nil)
	if len(results) != 1 || results[0].Context.OperationID != operationID || results[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("operation %s has %d results; the test requires one completed result", operationID, len(results))
	}
}

func runRecordedMigrate(t *testing.T, binary, dsn string, operator uuid.UUID, flags ...string) (string, error) {
	t.Helper()
	logDir := t.TempDir()
	arguments := append([]string{
		"--operator-id", operator.String(), "--operator-email", outcomeOperatorEmail, "migrate", "--execute",
	}, flags...)
	process := exec.CommandContext(context.WithoutCancel(t.Context()), binary, arguments...)
	process.Env = append(os.Environ(), "DATABASE_URL="+dsn, "AUDIT_OPERATOR_DSN="+dsn, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
	output, err := process.CombinedOutput()
	return string(output), err
}
