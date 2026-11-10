package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

const restoreVersionLogKey = "backup-status/fdb-versions.json"

type restoreVersionPoint struct {
	Version int64     `json:"version"`
	At      time.Time `json:"at"`
}

func TestRestoreVersionPrintsTheBytesOfTheAnswerAfterTheFinalEvent(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	bucket := testenv.ObjectStore(t)
	recorded := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	body, err := json.Marshal(map[string][]restoreVersionPoint{
		"points": {{Version: 1000, At: recorded}},
	})
	if err != nil {
		t.Fatalf("encode the version log: %v", err)
	}

	if _, err := objectStoreClient(bucket).PutObject(
		t.Context(),
		&s3.PutObjectInput{
			Bucket: aws.String(bucket.Bucket),
			Key:    aws.String(restoreVersionLogKey),
			Body:   bytes.NewReader(body),
		},
	); err != nil {
		t.Fatalf("upload the version log: %v", err)
	}

	configured := *ledger
	storeConfig := *ledger.cfg
	storeConfig.BackupS3Endpoint, storeConfig.BackupS3Region, storeConfig.BackupS3BucketMain = bucket.Endpoint, bucket.Region, bucket.Bucket
	storeConfig.BackupS3AccessKey, storeConfig.BackupS3SecretKey = bucket.AccessKey, bucket.SecretKey
	configured.cfg = &storeConfig
	operator := manifestRunOptions{
		readerDSN:  ledger.cfg.AuditReaderDSN,
		lifecycle:  rolePool(t, testenv.Ledger(t), "audit_operator"),
		operatorID: uuid.Must(uuid.NewV7()),
	}

	moment := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)

	var direct bytes.Buffer
	if err := ops.RunBackupFDBRestoreVersion(
		t.Context(),
		&storeConfig,
		moment,
		&direct,
	); err != nil {
		t.Fatalf("RunBackupFDBRestoreVersion: %v", err)
	}

	run := runOpsTreeCommand(
		t,
		&configured,
		operator,
		[]string{"ops", "backup", "restore-version"},
		"--at",
		moment.Format(time.RFC3339),
		"--execute",
	)
	if run.err != nil || run.stdout != direct.String() {
		t.Fatalf(
			"restore-version printed %q with error %v, want the bytes %q",
			run.stdout,
			run.err,
			direct.String(),
		)
	}

	row := awaitReadLifecycleRow(
		t,
		ledger,
		operator.operatorID,
		audit.VerbOpsBackupFDBRestoreVersion,
	)
	requireReadRowShape(t, row, audit.OutcomeCompleted, referenceTarget("backup/restore-version"))

	refusedOperator := operator
	refusedOperator.operatorID = uuid.Must(uuid.NewV7())

	refused := runOpsTreeCommand(
		t,
		&configured,
		refusedOperator,
		[]string{"ops", "backup", "restore-version"},
		"--at",
		recorded.Add(-time.Hour).Format(time.RFC3339),
		"--execute",
	)
	if refused.err == nil || !strings.Contains(refused.err.Error(), "oldest recorded reading") ||
		refused.stdout != "" {
		t.Fatalf(
			"restore-version before the oldest reading = %+v, want an error and no output",
			refused,
		)
	}

	requireFailedReadRow(
		t,
		awaitReadLifecycleRow(
			t,
			ledger,
			refusedOperator.operatorID,
			audit.VerbOpsBackupFDBRestoreVersion,
		),
		referenceTarget("backup/restore-version"),
		"oldest recorded reading",
	)

	if _, err := ledger.admin.Exec(
		t.Context(),
		readTestSQL(t, "runtime_audit_public_finalize_denied.sql"),
	); err != nil {
		t.Fatalf("configure the finalization-denied role: %v", err)
	}

	deniedOperator := operator
	deniedOperator.operatorID = uuid.Must(uuid.NewV7())
	deniedOperator.lifecycle = rolePool(t, testenv.Ledger(t), finalizeDeniedRole)

	denied := runOpsTreeCommand(
		t,
		&configured,
		deniedOperator,
		[]string{"ops", "backup", "restore-version"},
		"--at",
		moment.Format(time.RFC3339),
		"--execute",
	)
	if denied.err == nil || !strings.Contains(denied.err.Error(), "finalize audit read") ||
		denied.stdout != "" {
		t.Fatalf(
			"restore-version with a denied finalization = %+v, want a finalization error and no output",
			denied,
		)
	}

	recoverSearchRead(t, ledger.admin, deniedOperator.operatorID)
}
