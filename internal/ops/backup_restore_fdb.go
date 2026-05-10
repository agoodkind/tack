package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// restoreFDBArtifact replays one fdbbackup .tar.gz against a fresh scratch
// FDB cluster. The flow mirrors scripts/backup-restore-test.sh:231-372:
//
//  1. Extract the artifact into a host scratch dir
//  2. Locate the backup-* root inside it
//  3. Spin up a scratch FDB container with a fresh data volume
//  4. Wait for fdbcli status, configure new single memory if needed
//  5. Start backup_agent inside the scratch container
//  6. fdbrestore start --waitfordone against the backup URL
//  7. Unlock and range-scan to assert the cluster has data
func restoreFDBArtifact(ctx context.Context, r *restoreCtx, artifactPath string) error {
	label := filepath.Base(artifactPath)
	r.Log.InfoContext(ctx, "backup.restore_fdb.start", slog.String("artifact", label))

	extractDir, err := os.MkdirTemp("", "tack-rt-fdb-")
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.mktemp_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("mktemp extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	err = ExtractTarGz(artifactPath, extractDir)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.extract_failed",
			slog.String("artifact", label),
			slog.Any("err", err),
		)
		return fmt.Errorf("extract %s: %w", label, err)
	}

	backupRoot := findBackupSubdir(extractDir)
	if backupRoot == "" {
		noSubdirErr := fmt.Errorf("no backup-* subdir in extracted archive %s", label)
		r.Log.ErrorContext(ctx, "backup.restore_fdb.no_backup_subdir",
			slog.String("artifact", label),
			slog.Any("err", noSubdirErr),
		)
		return noSubdirErr
	}
	relRoot, err := filepath.Rel(extractDir, backupRoot)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.relpath_failed",
			slog.String("backup_root", backupRoot),
			slog.Any("err", err),
		)
		return fmt.Errorf("relpath %s: %w", backupRoot, err)
	}
	backupURL := "file:///restore/" + filepath.ToSlash(relRoot)

	containerName := "tack-rt-fdb-" + r.RunID
	volumeName := "tack-rt-fdb-data-" + r.RunID

	_, err = r.Cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volumeName})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.volume_create_failed",
			slog.String("name", volumeName),
			slog.Any("err", err),
		)
		return fmt.Errorf("volume create %s: %w", volumeName, err)
	}
	r.trackVolume(volumeName)

	err = startScratchFDB(ctx, r, containerName, volumeName, extractDir)
	if err != nil {
		return err
	}
	r.trackContainer(containerName)

	err = waitForFDBHealthy(ctx, r, containerName)
	if err != nil {
		return err
	}

	err = startBackupAgent(ctx, r, containerName)
	if err != nil {
		return err
	}

	err = runFDBRestoreInContainer(ctx, r, containerName, backupURL)
	if err != nil {
		return err
	}

	return assertFDBHasData(ctx, r, containerName)
}

// startScratchFDB launches a scratch FDB container with the FDB image,
// matching the env vars and mounts the shell script uses.
func startScratchFDB(ctx context.Context, r *restoreCtx, name, volumeName, extractDir string) error {
	cfg := &container.Config{
		Image: r.Cfg.BackupFDBImage,
		Env: []string{
			"FDB_NETWORKING_MODE=container",
			"FDB_PORT=4500",
			"FDB_COORDINATOR_PORT=4500",
			"FDB_CLUSTER_FILE_CONTENTS=docker:docker@127.0.0.1:4500",
		},
	}
	hostCfg := &container.HostConfig{
		Binds: []string{
			volumeName + ":/var/fdb/data",
			extractDir + ":/restore:ro",
		},
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       name,
	})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.create_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("create scratch fdb: %w", err)
	}
	_, err = r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.start_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("start scratch fdb: %w", err)
	}
	return nil
}

// waitForFDBHealthy waits for the cluster file to appear and configures a
// fresh single-memory cluster if status is unhealthy.
func waitForFDBHealthy(ctx context.Context, r *restoreCtx, name string) error {
	err := waitForExec(ctx, r.Cli, name, 30*time.Second,
		[]string{"test", "-f", "/var/fdb/fdb.cluster"})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.cluster_file_missing",
			slog.String("container", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("scratch fdb cluster file never appeared: %w", err)
	}
	res, err := containerExec(ctx, r.Cli, name,
		[]string{"sh", "-lc", "timeout 5 fdbcli --exec \"status minimal\""}, nil)
	if err != nil || res.ExitCode != 0 {
		_, err = containerExec(ctx, r.Cli, name,
			[]string{"sh", "-lc", "timeout 20 fdbcli --exec \"configure new single memory\""}, nil)
		if err != nil {
			r.Log.ErrorContext(ctx, "backup.restore_fdb.configure_failed",
				slog.String("container", name),
				slog.Any("err", err),
			)
			return fmt.Errorf("configure new single memory: %w", err)
		}
	}
	err = waitForExec(ctx, r.Cli, name, 60*time.Second,
		[]string{"sh", "-lc", "timeout 5 fdbcli --exec \"status minimal\""})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.health_timeout",
			slog.String("container", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("scratch fdb never reported healthy: %w", err)
	}
	return nil
}

// startBackupAgent kicks off the backup_agent inside the scratch container
// so fdbrestore has a worker draining the queue.
func startBackupAgent(ctx context.Context, r *restoreCtx, name string) error {
	res, err := containerExec(ctx, r.Cli, name,
		[]string{
			"sh", "-lc",
			"backup_agent --cluster-file /var/fdb/fdb.cluster --logdir /var/fdb/logs >/var/fdb/logs/backup_agent.out 2>&1 &",
		},
		nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.agent_exec_failed",
			slog.String("container", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("start backup_agent: %w", err)
	}
	if res.ExitCode != 0 {
		agentErr := fmt.Errorf("backup_agent exited %d: %s", res.ExitCode, res.Stderr)
		r.Log.ErrorContext(ctx, "backup.restore_fdb.agent_exit_nonzero",
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", res.Stderr),
			slog.Any("err", agentErr),
		)
		return agentErr
	}
	// Give the agent a moment to register with the cluster before fdbrestore
	// runs. Use a timer + select so context cancellation is honored.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		r.Log.ErrorContext(ctx, "backup.restore_fdb.agent_settle_cancelled",
			slog.String("container", name),
			slog.Any("err", ctx.Err()),
		)
		return fmt.Errorf("backup_agent settle wait: %w", ctx.Err())
	}
	return nil
}

// runFDBRestoreInContainer fires `fdbrestore start --waitfordone`. The
// 600-second cap matches the shell script.
func runFDBRestoreInContainer(ctx context.Context, r *restoreCtx, name, backupURL string) error {
	res, err := containerExec(ctx, r.Cli, name,
		[]string{
			"timeout", "600",
			"fdbrestore", "start",
			"--dest-cluster-file", "/var/fdb/fdb.cluster",
			"-r", backupURL,
			"--waitfordone",
		},
		nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.fdbrestore_exec_failed",
			slog.String("container", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("fdbrestore exec: %w", err)
	}
	if res.ExitCode != 0 {
		restoreErr := fmt.Errorf("fdbrestore exited %d", res.ExitCode)
		r.Log.ErrorContext(ctx, "backup.restore_fdb.restore_exit_nonzero",
			slog.Int("exit", res.ExitCode),
			slog.String("stdout", res.Stdout),
			slog.String("stderr", res.Stderr),
			slog.Any("err", restoreErr),
		)
		return restoreErr
	}
	return nil
}

// assertFDBHasData runs fdbcli getrange and verifies the cluster has at
// least one row.
func assertFDBHasData(ctx context.Context, r *restoreCtx, name string) error {
	statusRes, _ := containerExec(ctx, r.Cli, name,
		[]string{"sh", "-lc", "fdbrestore status --dest-cluster-file /var/fdb/fdb.cluster"}, nil)
	uid := extractRestoreUID(statusRes.Stdout + statusRes.Stderr)
	if uid != "" {
		_, _ = containerExec(ctx, r.Cli, name,
			[]string{"fdbcli", "--exec", "unlock " + uid}, nil)
	}

	res, err := containerExec(ctx, r.Cli, name,
		[]string{"fdbcli", "--exec", `getrange "" \xff 5`}, nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_fdb.getrange_exec_failed",
			slog.String("container", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("getrange exec: %w", err)
	}
	if !strings.Contains(res.Stdout, "Range limited to") &&
		!strings.Contains(res.Stdout, "`") {
		noMarkerErr := fmt.Errorf("getrange produced no recognizable output")
		r.Log.ErrorContext(ctx, "backup.restore_fdb.getrange_no_marker",
			slog.String("stdout", res.Stdout),
			slog.Any("err", noMarkerErr),
		)
		return noMarkerErr
	}
	return nil
}

// extractRestoreUID pulls the 32-hex UID out of an fdbrestore status line.
func extractRestoreUID(text string) string {
	const prefix = "UID: "
	_, after, found := strings.Cut(text, prefix)
	if !found {
		return ""
	}
	end := 0
	for end < len(after) && end < 32 {
		c := after[end]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			end++
			continue
		}
		break
	}
	if end != 32 {
		return ""
	}
	return after[:end]
}

// findBackupSubdir scans extractDir for a `backup-*` directory and returns
// the absolute path, or empty string if none is found.
func findBackupSubdir(extractDir string) string {
	var match string
	_ = filepath.Walk(extractDir, func(path string, info os.FileInfo, _ error) error {
		if info == nil || !info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), "backup-") && match == "" {
			match = path
		}
		return nil
	})
	return match
}
