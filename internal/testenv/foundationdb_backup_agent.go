package testenv

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// foundationDBRestorable is what `fdbbackup status` prints once the backup on
// the default tag can be restored, as in "The backup on tag `default' is
// restorable but continuing to <url>".
const foundationDBRestorable = " is restorable"

// StartFoundationDBBackupAgent starts a backup agent inside the container that
// serves the cluster. The agent runs until the test removes that container.
func StartFoundationDBBackupAgent(t *testing.T, cluster string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("create a Docker client for the FoundationDB backup agent: %v", err)
	}
	defer func() { _ = cli.Close() }()
	if err := startFDBBackupAgent(ctx, cli, FoundationDBContainer(cluster)); err != nil {
		t.Fatalf("start the FoundationDB backup agent: %v", err)
	}
}

// WaitForRestorableFoundationDBBackup polls `fdbbackup status` in the
// container that serves the cluster until the backup on the default tag is
// restorable. It does not print the status, because the status contains the
// object store credentials of a blobstore backup.
func WaitForRestorableFoundationDBBackup(t *testing.T, cluster string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("create a Docker client for the FoundationDB backup status: %v", err)
	}
	defer func() { _ = cli.Close() }()
	container := FoundationDBContainer(cluster)
	for {
		output, exitCode, err := execInContainer(ctx, cli, container,
			[]string{"fdbbackup", "status", "-C", containerClusterFile})
		if err != nil {
			t.Fatalf("read the FoundationDB backup status in %s: %v", container, err)
		}
		if exitCode == 0 && strings.Contains(output, foundationDBRestorable) {
			break
		}
		if !sleepOrDone(ctx) {
			t.Fatalf("the FoundationDB backup in %s did not become restorable before the deadline; the last exit code was %d",
				container, exitCode)
		}
	}
	slog.InfoContext(ctx, "testenv.foundationdb_backup.restorable", slog.String("container", container))
}
