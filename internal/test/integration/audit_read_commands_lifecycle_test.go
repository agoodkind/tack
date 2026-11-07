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
	readLifecycleNodePrefix = "tack:node/"
	readCommandsInvalidNode = "tack:node/invalid"
	readCommandsClassesRef  = "repair/classes"
	readCommandsBackupRef   = "backup"
)

func TestOperatorReadCommandsRecordReadLifecycle(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	operator := manifestRunOptions{
		readerDSN: ledger.cfg.AuditReaderDSN, lifecycle: rolePool(t, testenv.Ledger(t), "audit_operator"), operatorID: uuid.Nil,
	}

	classesOperator := operator
	classesOperator.operatorID = uuid.Must(uuid.NewV7())
	classes := runOpsTreeCommand(t, ledger, classesOperator, []string{"ops", "repair", "classes"}, "--execute")
	if classes.err != nil {
		t.Fatalf("ops repair classes failed: %+v", classes)
	}
	var classesOutput readLifecycleOutput
	if err := json.Unmarshal([]byte(classes.stdout), &classesOutput); err != nil {
		t.Fatalf("decode the ops repair classes output: %v\n%s", err, classes.stdout)
	}
	requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, classesOperator.operatorID, audit.VerbOpsRepairClasses),
		readCommandsClassesRef, classesOutput.Result, nil)

	backupOperator := operator
	backupOperator.operatorID = uuid.Must(uuid.NewV7())
	bare := runOpsTreeCommand(t, ledger, backupOperator, []string{"ops", "backup"}, "--execute")
	if bare.err == nil || !strings.Contains(bare.err.Error(), "subcommand") || bare.stdout != "" {
		t.Fatalf("bare ops backup = %+v, want a subcommand refusal and no result", bare)
	}
	requireFailedReadRow(t, awaitReadLifecycleRow(t, ledger, backupOperator.operatorID, audit.VerbOpsBackup), readCommandsBackupRef, "subcommand")

	now := clock.Now()
	entityID := uuid.Must(uuid.NewV7())
	window := []string{"--oldest", now.Add(-time.Hour).UTC().Format(time.RFC3339), "--latest", now.Add(time.Hour).UTC().Format(time.RFC3339)}
	changeArgs := append([]string{"audit", "last-change", "--org", uuid.Must(uuid.NewV7()).String(), "--entity-id", entityID.String()}, window...)
	changeOperator, changeRun := runReadLifecycleCommand(t, ledger, changeArgs...)
	if changeRun.err != nil {
		t.Fatalf("audit last-change failed: %v\n%s", changeRun.err, changeRun.output())
	}
	changeOutput := decodeReadLifecycleOutput(t, changeRun)
	requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, changeOperator, audit.VerbAuditRead),
		readLifecycleNodePrefix+entityID.String(), changeOutput.Result, nil)

	badArgs := append([]string{"audit", "last-change", "--org", uuid.Must(uuid.NewV7()).String(), "--entity-id", readLifecycleBadOrgFlag}, window...)
	badOperator, badRun := runReadLifecycleCommand(t, ledger, badArgs...)
	requireFailedReadRun(t, badRun)
	requireFailedReadRow(t, awaitReadLifecycleRow(t, ledger, badOperator, audit.VerbAuditRead), readCommandsInvalidNode, "--entity-id")
}
