//go:build runtimeaudit

package integration_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// coverageSnapshotIntervalSeconds is the snapshot interval of the
	// continuous backups in the coverage cases. A short interval makes the
	// first snapshot finish, and the backup restorable, within seconds.
	coverageSnapshotIntervalSeconds = 5
	// coverageRestoreKey is the key the restore-drill case writes to its
	// source cluster. The drill requires a restored keyspace that is not
	// empty.
	coverageRestoreKey = "tack-coverage-restore-drill"
	// skippedDrillLeg is the committed step that a drill without the
	// FoundationDB leg reports for its YugabyteDB leg.
	skippedDrillLeg = "restore-drill/yugabyte"
	// drillStepPrefix starts the external target reference of the drill
	// step, which ends with the backup bucket.
	drillStepPrefix = "backup/restore-drill/"
)

// skippedLegDrillCase runs restore-drill with continuous FoundationDB backup
// disabled. The drill skips the FoundationDB leg and records
// partially_completed with the YugabyteDB leg and the drill step as its
// committed steps.
func skippedLegDrillCase() coverageCase {
	drill := commandCase(audit.VerbOpsBackupRestoreDrill, audit.OutcomePartiallyCompleted,
		backupCommand("ops", "backup", "restore-drill"))
	drill.check = requireSkippedLegSteps
	return drill
}

// requireSkippedLegSteps requires the drill result to list the YugabyteDB leg
// and then the drill step in CommittedSteps.
func requireSkippedLegSteps(t *testing.T, env *coverageEnv, rows []audit.Row) {
	t.Helper()
	want := []string{skippedDrillLeg, drillStepPrefix + env.backup.cfg.BackupS3BucketMain}
	for _, row := range rows {
		result := row.Context.Phase == audit.PhaseResult && row.Context.ParentOperationID == uuid.Nil
		if !result || row.Action != string(audit.VerbOpsBackupRestoreDrill) || row.Outcome != audit.OutcomePartiallyCompleted {
			continue
		}
		if !slices.Equal(row.Context.CommittedSteps, want) {
			t.Fatalf("The drill result lists committed steps %q; the case requires %q", row.Context.CommittedSteps, want)
		}
		return
	}
	t.Fatalf("The ledger does not contain a partially_completed restore-drill result for the operator; the events are: %s", describeRows(rows))
}

// fdbBackupSource starts a FoundationDB cluster with one key and a backup
// agent. It returns the cluster file and a directory with a copy of the
// cluster file that the Docker daemon can bind-mount.
func fdbBackupSource(t *testing.T) (string, string) {
	t.Helper()
	clusterFile := testenv.FreshFoundationDB(t)
	testenv.ConfigureFoundationDB(t, clusterFile, "writemode on; set "+coverageRestoreKey+" present")
	testenv.StartFoundationDBBackupAgent(t, clusterFile)
	directory, _ := storeClusterDirectory(t, clusterFile)
	return clusterFile, directory
}

// continuousBackup returns configure with continuous FoundationDB backup
// enabled against the cluster file in directory.
func continuousBackup(directory string, configure func(*config.Config)) func(*config.Config) {
	return func(cfg *config.Config) {
		configure(cfg)
		cfg.BackupFDBContinuous, cfg.OpsFDBClusterDir = true, directory
		cfg.BackupFDBSnapshotInterval = coverageSnapshotIntervalSeconds
	}
}

// startContinuousBackup runs `ops backup fdb-continuous-init` with configure
// and fails the test when the command fails.
func (env *coverageEnv) startContinuousBackup(t *testing.T, configure func(*config.Config)) {
	t.Helper()
	if _, output, err := env.command(t, configure, "ops", "backup", "fdb-continuous-init"); err != nil {
		t.Fatalf("Starting the continuous backup failed; the case requires a running backup: %v\n%s", err, output)
	}
}

// continuousInitInput starts a continuous backup of a separate cluster into a
// new bucket. The case then runs fdb-continuous-init again. The second run
// must read the running backup through the configured cluster file directory
// and succeed without a second start.
func continuousInitInput(t *testing.T, env *coverageEnv) commandInput {
	_, directory := fdbBackupSource(t)
	bucket := testenv.ObjectStore(t)
	settings := objectStoreSettings(&config.Config{
		BackupS3Endpoint: bucket.Endpoint, BackupS3Region: bucket.Region, BackupS3BucketMain: bucket.Bucket,
		BackupS3AccessKey: bucket.AccessKey, BackupS3SecretKey: bucket.SecretKey,
	})
	input := backupCommand("ops", "backup", "fdb-continuous-init")(t, env)
	base := input.configure
	input.configure = continuousBackup(directory, func(cfg *config.Config) {
		base(cfg)
		settings(cfg)
	})
	env.startContinuousBackup(t, input.configure)
	return input
}

// restoreDrillInput starts a continuous backup of a separate cluster into the
// backup ledger's bucket and waits until the backup is restorable. The case
// then runs restore-drill with both legs.
func restoreDrillInput(t *testing.T, env *coverageEnv) commandInput {
	clusterFile, directory := fdbBackupSource(t)
	overlay, err := filepath.Abs(filepath.Join("..", "..", "..", "fdb-overlay", "fdb.bash"))
	if err != nil {
		t.Fatalf("Resolving the FoundationDB overlay failed; the drill requires its absolute path: %v", err)
	}
	input := backupCommand("ops", "backup", "restore-drill")(t, env)
	base := input.configure
	input.configure = continuousBackup(directory, func(cfg *config.Config) {
		base(cfg)
		cfg.BackupFDBOverlayPath = overlay
	})
	env.startContinuousBackup(t, input.configure)
	testenv.WaitForRestorableFoundationDBBackup(t, clusterFile)
	return input
}
