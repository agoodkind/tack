//go:build integration

package ops_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

type retentionFDBWindow struct {
	MinVersion int64 `json:"min_version"`
	MaxVersion int64 `json:"max_version"`
}

func retentionFDBTestConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	return retentionFDBTestConfigWithKnobs(t, cfg, nil)
}

func retentionFDBTestConfigWithKnobs(t *testing.T, cfg *config.Config, knobs map[string]string) string {
	t.Helper()
	cluster := testenv.FreshFoundationDBWithKnobs(t, knobs)
	cfg.BackupFDBImage = "foundationdb/foundationdb:7.4.6"
	cfg.BackupFDBNetwork = "tack-testenv"
	cfg.BackupFDBContinuous = true
	cfg.OpsFDBClusterDir = retentionFDBTestClusterDir(t, cluster)
	testenv.ConfigureFoundationDB(t, cluster, "writemode on; set retention-key retained-value")
	retentionFDBTestAgent(t, cluster)
	return cluster
}

func retentionFDBTestClusterDir(t *testing.T, cluster string) string {
	t.Helper()
	directory := testenv.SharedDir(t)
	contents, err := os.ReadFile(cluster)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fdb.cluster"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func retentionFDBTestAgent(t *testing.T, cluster string) {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	created, err := cli.ExecCreate(t.Context(), testenv.FoundationDBContainer(cluster), client.ExecCreateOptions{
		Cmd: []string{"backup_agent", "-C", "/var/fdb/fdb.cluster", "--logdir", "/var/fdb/logs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ExecStart(t.Context(), created.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
}

func retentionFDBTestURL(cfg *config.Config, name string) string {
	query := url.Values{"bucket": {cfg.BackupS3BucketMain}, "region": {cfg.BackupS3Region}, "secure_connection": {"0"}}
	destination := url.URL{
		Scheme: "blobstore", Host: strings.TrimPrefix(cfg.BackupS3Endpoint, "http://"), Path: "/" + name,
		User: url.UserPassword(cfg.BackupS3AccessKey, cfg.BackupS3SecretKey), RawQuery: query.Encode(),
	}
	return destination.String()
}

func retentionFDBTestStart(t *testing.T, cfg *config.Config, cluster string) string {
	t.Helper()
	return retentionFDBTestStartEvery(t, cfg, cluster, "5")
}

func retentionFDBTestStartEvery(t *testing.T, cfg *config.Config, cluster, snapshotSeconds string) string {
	t.Helper()
	name := time.Now().UTC().Format("20060102T150405Z")
	retentionFDBTestExec(t, cfg, cluster, []string{
		"fdbbackup", "start", "-C", "/var/fdb/fdb.cluster",
		"-d", retentionFDBTestURL(cfg, name), "-z", "--snapshot-interval", snapshotSeconds,
	})
	return name
}

// retentionFDBTestWindow waits until backup name is restorable past version
// after, and returns its restorable versions from describe without
// --version-timestamps.
func retentionFDBTestWindow(t *testing.T, cfg *config.Config, cluster, name string, after int64) retentionFDBWindow {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		output := retentionFDBTestExec(t, cfg, cluster, []string{
			"fdbbackup", "describe", "-d", retentionFDBTestURL(cfg, name), "-C", "/var/fdb/fdb.cluster",
		})
		minVersion, maxVersion, restorable, err := ops.RestorableVersionsFromDescribe(output)
		if err != nil {
			t.Fatalf("read the restorable window of backup %s: %v", name, err)
		}
		if restorable && maxVersion > after {
			return retentionFDBWindow{MinVersion: minVersion, MaxVersion: maxVersion}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backup %s did not establish a restorable window after version %d", name, after)
		case <-ticker.C:
		}
	}
}

func retentionFDBTestExec(t *testing.T, cfg *config.Config, cluster string, command []string) string {
	t.Helper()
	output, exitCode := retentionFDBTestExecResult(t, cfg, cluster, command)
	if exitCode != 0 {
		t.Fatal(fmt.Errorf("native %s exited %d: %s", command[0], exitCode, output))
	}
	return output
}

// retentionFDBTestExecResult runs command in the cluster container and returns
// its output, with the object store credentials redacted, and its exit code.
func retentionFDBTestExecResult(t *testing.T, cfg *config.Config, cluster string, command []string) (string, int) {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	created, err := cli.ExecCreate(t.Context(), testenv.FoundationDBContainer(cluster), client.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := cli.ExecAttach(t.Context(), created.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		t.Fatal(err)
	}
	state, err := cli.ExecInspect(t.Context(), created.ID, client.ExecInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String() + stderr.String()
	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		output = strings.ReplaceAll(output, credential, "[redacted]")
	}
	return output, state.ExitCode
}
