package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// FoundationDBBackup is a completed snapshot of the test process's cluster.
type FoundationDBBackup struct {
	containerName string
	url           string
	directory     string
}

// BackupFoundationDB runs fdbbackup from the pinned engine image and returns after the snapshot completes.
func BackupFoundationDB(t T) FoundationDBBackup {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	name := filepath.Base(filepath.Dir(FoundationDB(t)))
	cli, err := dockerClient(ctx)
	if err != nil {
		failFDBBackup(t, err)
	}
	defer func() { _ = cli.Close() }()
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		failFDBBackup(t, err)
	}
	directory := "/var/fdb/search-acceptance-backup-" + suffix
	if err := startFDBBackupAgent(ctx, cli, name); err != nil {
		failFDBBackup(t, err)
	}
	command := []string{
		"fdbbackup", "start", "-C", containerClusterFile,
		"-d", "file://" + directory, "-w",
	}
	output, err := runFDBBackupCommand(ctx, cli, name, command)
	if err != nil {
		failFDBBackup(t, err)
	}
	_, _ = fmt.Fprintln(t.Output(), "FoundationDB snapshot completed:", strings.TrimSpace(output))
	status, err := runFDBBackupCommand(ctx, cli, name, []string{"fdbbackup", "status", "-C", containerClusterFile})
	if err != nil {
		failFDBBackup(t, err)
	}
	if !strings.Contains(strings.ToLower(status), "completed") {
		failFDBBackup(t, fmt.Errorf("snapshot has no completed status: %s", status))
	}
	_, _ = fmt.Fprintln(t.Output(), "FoundationDB snapshot status:", strings.TrimSpace(status))
	output, err = runFDBBackupCommand(ctx, cli, name,
		[]string{"find", directory, "-maxdepth", "1", "-type", "d", "-name", "backup-*"})
	if err != nil {
		failFDBBackup(t, err)
	}
	paths := strings.Fields(output)
	if len(paths) != 1 {
		failFDBBackup(t, fmt.Errorf("snapshot contains %d backup directories, want one: %s", len(paths), output))
	}
	return FoundationDBBackup{containerName: name, url: "file://" + paths[0], directory: directory}
}

// Restore restores the snapshot into a fresh test cluster and returns its file.
// The test removes the restored cluster when it ends.
func (backup FoundationDBBackup) Restore(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cluster, err := provisionFoundationDB(ctx)
	if err != nil {
		failFDBBackup(t, err)
	}
	removeFoundationDBAfterTest(t, cluster)
	target := filepath.Base(filepath.Dir(cluster))
	cli, err := dockerClient(ctx)
	if err != nil {
		failFDBBackup(t, err)
	}
	defer func() { _ = cli.Close() }()
	copied, err := cli.CopyFromContainer(ctx, backup.containerName, client.CopyFromContainerOptions{SourcePath: backup.directory})
	if err != nil {
		failFDBBackup(t, fmt.Errorf("copy completed snapshot: %w", err))
	}
	defer func() { _ = copied.Content.Close() }()
	if _, err := cli.CopyToContainer(ctx, target, client.CopyToContainerOptions{
		DestinationPath: "/var/fdb", Content: copied.Content, AllowOverwriteDirWithFile: false, CopyUIDGID: true,
	}); err != nil {
		failFDBBackup(t, fmt.Errorf("copy snapshot into fresh cluster: %w", err))
	}
	if err := startFDBBackupAgent(ctx, cli, target); err != nil {
		failFDBBackup(t, err)
	}
	output, err := runFDBBackupCommand(ctx, cli, target, []string{
		"fdbrestore", "start", "--dest-cluster-file", containerClusterFile, "-r", backup.url, "--waitfordone",
	})
	if err != nil {
		failFDBBackup(t, err)
	}
	_, _ = fmt.Fprintln(t.Output(), "FoundationDB snapshot restored into", target, ":", strings.TrimSpace(output))
	return cluster
}

func startFDBBackupAgent(ctx context.Context, cli *client.Client, name string) error {
	created, err := cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		Cmd:          []string{"backup_agent", "-C", containerClusterFile, "--logdir", "/var/fdb/logs"},
		AttachStdout: false, AttachStderr: false,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.snapshot.agent_create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create snapshot agent in %s: %w", name, err)
	}
	_, err = cli.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true, TTY: false})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.snapshot.agent_start_failed", slog.String("err", err.Error()))
		return fmt.Errorf("start snapshot agent in %s: %w", name, err)
	}
	return nil
}

func runFDBBackupCommand(ctx context.Context, cli *client.Client, name string, command []string) (string, error) {
	output, exitCode, err := execInContainer(ctx, cli, name, command)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		failure := fmt.Errorf("%s in %s exited %d: %s", command[0], name, exitCode, output)
		slog.ErrorContext(ctx, "testenv.snapshot.command_failed", slog.String("err", failure.Error()), slog.String("container", name))
		return "", failure
	}
	return output, nil
}

func failFDBBackup(t T, err error) {
	_, _ = fmt.Fprintln(t.Output(), "FoundationDB snapshot failed:", err)
	t.FailNow()
}
