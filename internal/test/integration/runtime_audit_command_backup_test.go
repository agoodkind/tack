package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

type stalenessStepEvidence struct {
	Bucket   string `json:"bucket"`
	FDBLeg   string `json:"fdb_leg"`
	Readings []struct {
		Name    string `json:"name"`
		Verdict string `json:"verdict"`
	} `json:"readings"`
	Stale []string `json:"stale"`
}

func testArchiveNodeRefused(t *testing.T) {
	pool := ledgerAdminPool(t)
	cfg := backupBucketConfig(testenv.ObjectStore(t))
	operatorID := uuid.Must(uuid.NewV7())
	if _, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "backup", "yb-archive-node"); err == nil {
		t.Fatal("yb-archive-node succeeded without TACK_BACKUP_YB_ROCKSDB_DIR")
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsBackupYBArchiveNode), operatorID.String())
	requireOnlyResult(t, events, audit.OutcomeRefused, "configuration_incomplete")
}

func testStalenessCheckOutcome(t *testing.T) {
	pool := ledgerAdminPool(t)
	cfg := backupBucketConfig(testenv.ObjectStore(t))
	cfg.BackupRoot = t.TempDir()
	operatorID := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "backup", "buckets-init"); err != nil {
		t.Fatalf("buckets-init failed: %v\n%s", err, output)
	}
	_, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "backup", "staleness-check")
	if err == nil || !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf("staleness-check returned %v; the test requires an error containing \"past threshold\" for an empty backup bucket", err)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsBackupStalenessCheck), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("staleness-check recorded %d intents instead of 1", len(intents))
	}
	operationID := intents[0].Context.OperationID
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(stepResults) != 1 || stepResults[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("staleness-check recorded %d step results; the test requires one completed step result", len(stepResults))
	}
	var evidence stalenessStepEvidence
	if err := json.Unmarshal(stepResults[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding the staleness evidence failed: %v", err)
	}
	if evidence.Bucket != cfg.BackupS3BucketMain || evidence.FDBLeg != "skipped" || len(evidence.Readings) == 0 {
		t.Fatalf("staleness-check recorded %+v; the test requires bucket %s, FDBLeg \"skipped\", and at least one reading",
			evidence, cfg.BackupS3BucketMain)
	}
	for _, reading := range evidence.Readings {
		if reading.Verdict != "STALE" || !slices.Contains(evidence.Stale, reading.Name) {
			t.Fatalf("Reading %+v must have verdict STALE and appear in the stale readings %v", reading, evidence.Stale)
		}
	}
	requireCompletedResult(t, events, operationID)
}
