//go:build runtimeaudit

package integration_test

import (
	"path/filepath"
	"testing"

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
)

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
