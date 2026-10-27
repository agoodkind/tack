//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

// coverageBackupLedger groups a separate YugabyteDB node with the configuration
// for backup and ledger command cases.
type coverageBackupLedger struct {
	node testenv.EmptyLedgerNode
	cfg  *config.Config
}

// startCoverageBackupLedger starts an empty YugabyteDB node, applies
// migrations, and seeds audit roles, auth rows, and an audit event. It exports
// a snapshot and archives the node for backup command cases.
func startCoverageBackupLedger(t *testing.T) *coverageBackupLedger {
	t.Helper()
	ctx := t.Context()
	node := testenv.StartEmptyLedger(t, drillLedgerContainer)
	if err := migration.Migrate(ctx, node.DSN, migrations.FS); err != nil {
		t.Fatalf("Backup ledger migration failed; the test requires a migrated database: %v", err)
	}
	cfg := restoreDrillConfig(t, node)
	if err := ops.RunAuditSeedRoles(ctx, cfg); err != nil {
		t.Fatalf("Backup ledger setup failed to seed audit roles; the test requires those roles: %v", err)
	}
	admin, err := pgxpool.New(ctx, node.DSN)
	if err != nil {
		t.Fatalf("Backup ledger setup failed to create a connection pool; the test requires database access: %v", err)
	}
	t.Cleanup(admin.Close)
	seedDrillAuthRows(t, admin)
	seedDrillLedgerRow(t, node.DSN, admin)
	if err := ops.RunBackupYBSnapshotExport(ctx, cfg); err != nil {
		t.Fatalf("Snapshot export failed; the backup cases require an exported snapshot: %v", err)
	}
	if err := ops.RunBackupYBArchiveNode(ctx, cfg, ""); err != nil {
		t.Fatalf("Node archiving failed; the backup cases require an archived ledger node: %v", err)
	}
	cfg.LedgerNodeHosts = drillLedgerContainer + "=" + node.Address
	return &coverageBackupLedger{node: node, cfg: cfg}
}

// backupCommand prepares command input with the separate backup ledger's
// database, node, and object storage settings. It disables continuous
// FoundationDB backups unless a case enables them.
func backupCommand(args ...string) func(*testing.T, *coverageEnv) commandInput {
	return func(_ *testing.T, env *coverageEnv) commandInput {
		backup := env.backup.cfg
		return commandInput{args: args, outbox: nil, configure: func(cfg *config.Config) {
			cfg.DatabaseURL, cfg.YugabyteDB, cfg.BackupRoot = backup.DatabaseURL, backup.YugabyteDB, backup.BackupRoot
			cfg.BackupYBImage, cfg.BackupYBOverlayPath, cfg.BackupYBRocksDBDir = backup.BackupYBImage, backup.BackupYBOverlayPath, backup.BackupYBRocksDBDir
			cfg.BackupFDBNetwork, cfg.BackupFDBContinuous, cfg.BackupYBMasterAddresses = backup.BackupFDBNetwork, false, backup.BackupYBMasterAddresses
			cfg.BackupS3Endpoint, cfg.BackupS3Region, cfg.BackupS3BucketMain = backup.BackupS3Endpoint, backup.BackupS3Region, backup.BackupS3BucketMain
			cfg.BackupS3AccessKey, cfg.BackupS3SecretKey = backup.BackupS3AccessKey, backup.BackupS3SecretKey
			cfg.MigratorPassword, cfg.LedgerNodeHosts = backup.MigratorPassword, backup.LedgerNodeHosts
		}}
	}
}

// addBackupCommandCases adds backup command cases with their expected audit
// verbs and outcomes, including cases that expect command failure.
func addBackupCommandCases(cases map[string]coverageCase) {
	cases["command ops backup retention"] = commandCase(audit.VerbOpsBackupRetention, audit.OutcomeCompleted,
		backupCommand("ops", "backup", "retention"))
	cases["command ops backup"] = failingCommandCase(audit.VerbOpsBackup, audit.OutcomeOK, fixedCommand("ops", "backup"))
	cases["command ops backup yb-pitr-init"] = commandCase(audit.VerbOpsBackupYBPITRInit, audit.OutcomeCompleted, backupCommand("ops", "backup", "yb-pitr-init"))
	cases["command ops backup yb-snapshot-export"] = commandCase(audit.VerbOpsBackupYBSnapshotExport, audit.OutcomeCompleted,
		backupCommand("ops", "backup", "yb-snapshot-export"))
	cases["command ops backup restore-drill"] = commandCase(audit.VerbOpsBackupRestoreDrill, audit.OutcomeCompleted, restoreDrillInput)
	cases["command ops backup restore-drill"+caseVariantSeparator+"fdb-leg-skipped"] = skippedLegDrillCase()
	cases["command ops backup restore-version"] = commandCase(audit.VerbOpsBackupFDBRestoreVersion, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			env.writeVersionLog(t)
			return backupCommand("ops", "backup", "restore-version", "--at", time.Now().UTC().Format(time.RFC3339))(t, env)
		})
	cases["command ops backup yb-archive-node"] = commandCase(audit.VerbOpsBackupYBArchiveNode, audit.OutcomeCompleted,
		backupCommand("ops", "backup", "yb-archive-node"))
	cases["command ops backup staleness-check"] = failingCommandCase(audit.VerbOpsBackupStalenessCheck, audit.OutcomeCompleted,
		emptyBucketStalenessInput)
	cases["command ops backup fdb-continuous-init"] = commandCase(audit.VerbOpsBackupFDBContinuousInit, audit.OutcomeCompleted,
		continuousInitInput)
}

// emptyBucketStalenessInput initializes a new backup bucket that does not
// contain any backup. The staleness check reports a stale verdict as a
// command error and records a completed outcome.
func emptyBucketStalenessInput(t *testing.T, env *coverageEnv) commandInput {
	bucket := backupBucketConfig(testenv.ObjectStore(t))
	root := t.TempDir()
	settings := objectStoreSettings(bucket)
	configure := func(cfg *config.Config) {
		settings(cfg)
		cfg.BackupRoot = root
	}
	if _, output, err := env.command(t, configure, "ops", "backup", "buckets-init"); err != nil {
		t.Fatalf("Bucket initialization failed; the staleness case requires an initialized empty bucket: %v\n%s", err, output)
	}
	return commandInput{args: []string{"ops", "backup", "staleness-check"}, outbox: nil, configure: configure}
}

// writeVersionLog uploads a FoundationDB version point from one hour earlier to
// object storage for the restore-version case.
func (env *coverageEnv) writeVersionLog(t *testing.T) {
	t.Helper()
	backup := env.backup.cfg
	body, err := json.Marshal(map[string][]map[string]any{
		"points": {{"version": 1000, "at": time.Now().UTC().Add(-time.Hour)}},
	})
	if err != nil {
		t.Fatalf("Version log encoding failed; the restore-version case requires a JSON version log: %v", err)
	}
	client := objectStoreClient(testenv.ObjectStoreBucket{
		Endpoint: backup.BackupS3Endpoint, Region: backup.BackupS3Region, Bucket: backup.BackupS3BucketMain,
		AccessKey: backup.BackupS3AccessKey, SecretKey: backup.BackupS3SecretKey,
	})
	if _, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(backup.BackupS3BucketMain), Key: aws.String("backup-status/fdb-versions.json"), Body: bytes.NewReader(body),
	}); err != nil {
		t.Fatalf("Version log upload failed; the restore-version case requires the log in object storage: %v", err)
	}
}

// addLedgerCommandCases adds ledger node preparation and readiness cases
// against the separate backup ledger.
func addLedgerCommandCases(cases map[string]coverageCase) {
	cases["command ops ledger node-prepare"] = commandCase(audit.VerbOpsLedgerNodePrepare, audit.OutcomeCompleted, backupCommand("ops", "ledger", "node-prepare"))
	cases["command ops ledger node-wait"] = commandCase(audit.VerbOpsLedgerNodeWait, audit.OutcomeCompleted,
		backupCommand("ops", "ledger", "node-wait", "--stall", "3m", "--poll", "2s"))
	cases["command ops ledger bootstrap-wait"] = commandCase(audit.VerbOpsLedgerBootstrapWait, audit.OutcomeOK,
		backupCommand("ops", "ledger", "bootstrap-wait", "--masters", "1", "--tablet-servers", "1"))
}
