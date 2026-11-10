//go:build integration

package ops_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

func TestBackupRetentionCancellationRemovesNativeReaderBeforeUnlock(t *testing.T) {
	fixture := newRetentionCommandFixture(t)
	cluster := retentionFDBTestConfig(t, fixture.config)
	fixture.store.put("backups/20261001T000000Z", nil)
	// The plan converts the cutoff with a version log reading before its
	// first native read.
	retentionFDBTestAppendReading(t, fixture, retentionFDBTestReading{Version: 1, At: fixture.cutoff.Add(-time.Hour)})
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.ContainerStop(t.Context(), testenv.FoundationDBContainer(cluster), client.ContainerStopOptions{}); err != nil {
		t.Fatalf("stop the temporary source cluster: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := fixture.runContext(t, ctx, true); finished <- err }()
	reader := waitForRetentionReader(t, cli, fixture.config.OpsFDBClusterDir)
	state, err := cli.ContainerInspect(t.Context(), reader, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal("inspect the synthetic native reader")
	}
	if problem := checkRetentionMetadata(t.Context(), cli, reader, fixture.config, state); problem != "" {
		t.Fatal(problem)
	}
	restoreContext, cancelRestore := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancelRestore()
	if err := ops.RunBackupRestoreDrill(restoreContext, fixture.config, ops.RestoreDrillOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restore did not wait while the native reader ran: %v", err)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled native reader returned %v", err)
	}
	if _, err := cli.ContainerInspect(t.Context(), reader, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("native reader remains after retention returned: %v", err)
	}
	fixture.config.BackupFDBContinuous = false
	if err := ops.PreviewBackupRetention(t.Context(), fixture.config, ops.BackupRetentionOptions{KeepDays: 14}, io.Discard); err != nil {
		t.Fatalf("retention did not release its lock after removing the native reader: %v", err)
	}
}

func waitForRetentionReader(t *testing.T, cli *client.Client, source string) string {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		listed, err := cli.ContainerList(t.Context(), client.ContainerListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range listed.Items {
			if !strings.Contains(item.Command, "fdbbackup") {
				continue
			}
			state, err := cli.ContainerInspect(t.Context(), item.ID, client.ContainerInspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, bind := range state.Container.HostConfig.Binds {
				if strings.HasPrefix(bind, source+":") {
					return item.ID
				}
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("retention did not start its native source reader")
		case <-ticker.C:
		}
	}
}
