package integration_test

import (
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
	if _, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"backup",
		"yb-archive-node",
	); err == nil {
		t.Fatal("yb-archive-node succeeded without TACK_BACKUP_YB_ROCKSDB_DIR")
	}

	events := outboxEventsByQuery(
		t,
		pool,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsBackupYBArchiveNode),
		operatorID.String(),
	)
	requireOnlyResult(t, events, audit.OutcomeRefused, "configuration_incomplete")
}

func testStalenessCheckOutcome(t *testing.T) {
	pool := ledgerAdminPool(t)
	cfg := backupBucketConfig(testenv.ObjectStore(t))
	cfg.BackupRoot = t.TempDir()
	operatorID := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"backup",
		"buckets-init",
	); err != nil {
		t.Fatalf("buckets-init failed: %v\n%s", err, output)
	}

	_, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"backup",
		"staleness-check",
	)
	if err == nil || !strings.Contains(err.Error(), "past threshold") {
		t.Fatalf(
			"staleness-check returned %v; the test requires an error containing \"past threshold\" for an empty backup bucket",
			err,
		)
	}

	events := outboxEventsByQuery(
		t,
		pool,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsBackupStalenessCheck),
		operatorID.String(),
	)
	intents := eventsInPhase(events, audit.PhaseAttempt, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("staleness-check recorded %d intents instead of 1", len(intents))
	}

	operationID := intents[0].Context.OperationID
	steps := eventsInPhase(events, audit.PhaseAttempt, operationID)
	if len(steps) != 1 || steps[0].Outcome != audit.OutcomeAttempted {
		t.Fatalf("staleness-check recorded %d step attempts; want one", len(steps))
	}

	requireCompletedResult(t, events, operationID)
}
