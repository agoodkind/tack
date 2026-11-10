package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	readCommandsClassesRef = "repair/classes"
	readCommandsBackupRef  = "backup"
)

func TestOperatorReadCommandsRecordReadLifecycle(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	operatorOptions := func() manifestRunOptions {
		return manifestRunOptions{
			readerDSN: ledger.cfg.AuditReaderDSN, lifecycle: rolePool(t, testenv.Ledger(t), "audit_operator"), operatorID: uuid.Must(uuid.NewV7()),
		}
	}

	t.Run("ops repair classes", func(t *testing.T) {
		operator := operatorOptions()
		classes := runOpsTreeCommand(t, ledger, operator, []string{"ops", "repair", "classes"}, "--execute")
		if classes.err != nil {
			t.Fatalf("ops repair classes failed: %+v", classes)
		}
		var output readLifecycleOutput
		if err := json.Unmarshal([]byte(classes.stdout), &output); err != nil {
			t.Fatalf("decode the ops repair classes output: %v\n%s", err, classes.stdout)
		}
		requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, operator.operatorID, audit.VerbOpsRepairClasses),
			referenceTarget(readCommandsClassesRef), output.Result, nil)
	})

	t.Run("ops backup without a subcommand", func(t *testing.T) {
		operator := operatorOptions()
		bare := runOpsTreeCommand(t, ledger, operator, []string{"ops", "backup"}, "--execute")
		if bare.err == nil || !strings.Contains(bare.err.Error(), "subcommand") || bare.stdout != "" {
			t.Fatalf("bare ops backup = %+v, want a subcommand refusal and no result", bare)
		}
		requireFailedReadRow(t, awaitReadLifecycleRow(t, ledger, operator.operatorID, audit.VerbOpsBackup),
			referenceTarget(readCommandsBackupRef), "subcommand")
	})

	now := clock.Now()
	window := []string{"--oldest", now.Add(-time.Hour).UTC().Format(time.RFC3339), "--latest", now.Add(time.Hour).UTC().Format(time.RFC3339)}

	t.Run("audit last-change", func(t *testing.T) {
		entityID := uuid.Must(uuid.NewV7())
		args := append([]string{"audit", "last-change", "--org", uuid.Must(uuid.NewV7()).String(), "--entity-id", entityID.String()}, window...)
		operatorID, run := runReadLifecycleCommand(t, ledger, args...)
		if run.err != nil {
			t.Fatalf("audit last-change failed: %v\n%s", run.err, run.output())
		}
		output := decodeReadLifecycleOutput(t, run)
		requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditRead),
			uriTarget(audit.NodeURIPrefix+entityID.String()), output.Result, nil)
	})

	t.Run("audit last-change with a malformed entity", func(t *testing.T) {
		args := append([]string{"audit", "last-change", "--org", uuid.Must(uuid.NewV7()).String(), "--entity-id", readLifecycleBadOrgFlag}, window...)
		operatorID, run := runReadLifecycleCommand(t, ledger, args...)
		requireFailedReadRun(t, run)
		requireFailedReadRow(t, awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditRead),
			uriTarget(audit.NodeURIPrefix+audit.InvalidURISegment), "--entity-id")
	})
}
