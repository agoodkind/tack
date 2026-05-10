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

// runBackupFDB performs a `fdbbackup`-driven snapshot. The 2026-05-09
// rebuild proved that a named-volume tar of /var/fdb/data is silently
// empty because the FDB image declares `VOLUME /var/fdb/data` and an
// anonymous volume shadows the bind. The fix: drive an actual fdbbackup
// session against the live cluster, with a backup_agent sidecar to drain
// the queue, then assert `Restorable: true` against the timestamped
// subdirectory before we trust the artifact.
//
// All four 2026-05-09 fixes preserved:
//   - fdbbackup, not volume tar (anonymous-volume shadowing)
//   - backup_agent sidecar with `--entrypoint /usr/bin/backup_agent`
//   - fdbbackup describe targeting the resolved `backup-*` subdir
//   - assert `Restorable: true` or fail loudly
func runBackupFDB(ctx context.Context, b *backupCtx) error {
	sidecarName := b.Cfg.BackupFDBSidecar
	// Pre-clean any sidecar from a prior aborted run; the helper is
	// no-op when nothing matches.
	removeContainerForce(ctx, b.Cli, sidecarName)

	// Use a derived background context for teardown so signal cancellation
	// of ctx still lets us clean up the sidecar.
	defer func() {
		teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		removeContainerForce(teardown, b.Cli, sidecarName)
	}()

	err := startFDBSidecar(ctx, b, sidecarName)
	if err != nil {
		return err
	}

	timeoutSec := b.Cfg.BackupFDBTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}
	startCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	err = runFDBBackupStart(startCtx, b)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.start_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("fdbbackup start: %w", err)
	}

	backupURL, subdir, err := resolveFDBBackupSubdir(ctx, b.Log, b.SnapshotDir, b.RunID)
	if err != nil {
		return err
	}
	b.Log.InfoContext(ctx, "backup.fdb.subdir_resolved",
		slog.String("backup_url", backupURL),
		slog.String("subdir", subdir),
	)

	describeOut, err := runFDBBackupDescribe(ctx, b, backupURL)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.describe_failed",
			slog.String("backup_url", backupURL),
			slog.String("output", describeOut),
			slog.Any("err", err),
		)
		return fmt.Errorf("fdbbackup describe: %w (output: %s)", err, describeOut)
	}
	if !strings.Contains(describeOut, "Restorable: true") {
		notRestorableErr := fmt.Errorf("fdbbackup describe did not report Restorable: true")
		b.Log.ErrorContext(ctx, "backup.fdb.not_restorable",
			slog.String("backup_url", backupURL),
			slog.String("describe", describeOut),
			slog.Any("err", notRestorableErr),
		)
		return notRestorableErr
	}

	// Persist the describe output alongside the backup so verify and
	// restore-test can inspect what fdbbackup saw at snapshot time.
	describePath := filepath.Join(b.DestDir, "fdb", "describe.txt")
	err = os.MkdirAll(filepath.Dir(describePath), 0o750)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.mkdir_failed",
			slog.String("dir", filepath.Dir(describePath)),
			slog.Any("err", err),
		)
		return fmt.Errorf("mkdir fdb dir: %w", err)
	}
	err = os.WriteFile(describePath, []byte(describeOut), 0o600)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.describe_write_failed",
			slog.String("path", describePath),
			slog.Any("err", err),
		)
		return fmt.Errorf("write describe.txt: %w", err)
	}

	tarPath := filepath.Join(b.DestDir, "fdb-snapshot.tar.gz")
	err = tarFDBBackupSubdir(ctx, b, subdir, tarPath)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.tar_failed",
			slog.String("subdir", subdir),
			slog.String("tar_path", tarPath),
			slog.Any("err", err),
		)
		return fmt.Errorf("tar fdb backup subdir: %w", err)
	}
	b.Log.InfoContext(ctx, "backup.fdb.complete", slog.String("artifact", tarPath))
	return nil
}

// startFDBSidecar runs a backup_agent container on the FDB cluster's
// network, mounting the cluster file read-only and the snapshot dir as
// the destination. The image's default entrypoint exits immediately, which
// is why we override with `/usr/bin/backup_agent` (proven 2026-05-09).
func startFDBSidecar(ctx context.Context, b *backupCtx, name string) error {
	cfg := &container.Config{
		Image:      b.Cfg.BackupFDBImage,
		Entrypoint: []string{"/usr/bin/backup_agent"},
		Cmd:        []string{"-C", "/etc/foundationdb/fdb.cluster"},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false,
		Binds: []string{
			"/etc/foundationdb:/etc/foundationdb:ro",
			b.SnapshotDir + ":/snapshot",
		},
	}
	created, err := b.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: netMode(b.Cfg.BackupFDBNetwork),
		Name:             name,
	})
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.sidecar_create_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("create sidecar %s: %w", name, err)
	}
	_, err = b.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.sidecar_start_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("start sidecar %s: %w", name, err)
	}
	// Readiness probe: pgrep -x backup_agent inside the sidecar. Matches
	// scripts/backup-functions.sh:62.
	err = waitForExec(ctx, b.Cli, name, 30*time.Second, []string{"pgrep", "-x", "backup_agent"})
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.sidecar_not_ready",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("backup_agent did not become ready in %s: %w", name, err)
	}
	b.Log.InfoContext(ctx, "backup.fdb.sidecar_ready", slog.String("name", name))
	return nil
}

// runFDBBackupStart fires `fdbbackup start -w` from a one-shot client
// container that joins the same docker network as the live cluster.
func runFDBBackupStart(ctx context.Context, b *backupCtx) error {
	res, err := runOneShot(ctx, b.Cli, b.Log, runOneShotOptions{
		Image:      b.Cfg.BackupFDBImage,
		Network:    b.Cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/fdbbackup"},
		Cmd:        []string{"start", "-w", "-d", "file:///snapshot/" + b.RunID},
		Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
		Binds: []string{
			"/etc/foundationdb:/etc/foundationdb:ro",
			b.SnapshotDir + ":/snapshot",
		},
		Name: "",
	})
	if err != nil {
		return err
	}
	b.Log.DebugContext(ctx, "backup.fdb.start_out", slog.String("output", res.Stdout))
	if res.ExitCode != 0 {
		return fmt.Errorf("fdbbackup start exited %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// resolveFDBBackupSubdir locates the timestamped `backup-*` subdir that
// fdbbackup creates under /snapshot/<run-id>/. The describe URL must point
// at this subdirectory; pointing at the parent reports Restorable: false.
// Mirrors `tack_backup_resolve_fdb_subdir` in backup-functions.sh:91-102.
func resolveFDBBackupSubdir(ctx context.Context, log *slog.Logger, snapshotDir, runID string) (string, string, error) {
	root := filepath.Join(snapshotDir, runID)
	var match string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			log.ErrorContext(ctx, "backup.fdb.subdir_walk_entry_failed",
				slog.String("path", path),
				slog.Any("err", walkErr),
			)
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if !info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "backup-") && match == "" {
			match = path
		}
		return nil
	})
	if err != nil {
		log.ErrorContext(ctx, "backup.fdb.subdir_walk_failed",
			slog.String("root", root),
			slog.Any("err", err),
		)
		return "", "", fmt.Errorf("walk %s: %w", root, err)
	}
	if match == "" {
		noSubdirErr := fmt.Errorf("no backup-* subdir under %s", root)
		log.ErrorContext(ctx, "backup.fdb.no_subdir",
			slog.String("root", root),
			slog.Any("err", noSubdirErr),
		)
		return "", "", noSubdirErr
	}
	rel, err := filepath.Rel(snapshotDir, match)
	if err != nil {
		log.ErrorContext(ctx, "backup.fdb.relpath_failed",
			slog.String("path", match),
			slog.Any("err", err),
		)
		return "", "", fmt.Errorf("relpath: %w", err)
	}
	url := "file:///snapshot/" + filepath.ToSlash(rel)
	return url, match, nil
}

// runFDBBackupDescribe runs `fdbbackup describe -d <url>` from a one-shot
// client container and returns the captured output.
func runFDBBackupDescribe(ctx context.Context, b *backupCtx, backupURL string) (string, error) {
	res, err := runOneShot(ctx, b.Cli, b.Log, runOneShotOptions{
		Image:      b.Cfg.BackupFDBImage,
		Network:    b.Cfg.BackupFDBNetwork,
		Entrypoint: []string{"/usr/bin/fdbbackup"},
		Cmd:        []string{"describe", "-d", backupURL},
		Env:        []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"},
		Binds: []string{
			"/etc/foundationdb:/etc/foundationdb:ro",
			b.SnapshotDir + ":/snapshot",
		},
		Name: "",
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("fdbbackup describe exited %d: %s", res.ExitCode, res.Stderr)
	}
	return res.Stdout, nil
}

// tarFDBBackupSubdir tars the resolved backup-* subdir into a single
// .tar.gz under DestDir. Uses an alpine one-shot so the operator's host
// does not need GNU tar; the production host uses busybox tar via this
// path uniformly.
func tarFDBBackupSubdir(ctx context.Context, b *backupCtx, hostSubdir, tarPath string) error {
	rel, err := filepath.Rel(b.SnapshotDir, hostSubdir)
	if err != nil {
		b.Log.ErrorContext(ctx, "backup.fdb.tar_relpath_failed",
			slog.String("hostSubdir", hostSubdir),
			slog.Any("err", err),
		)
		return fmt.Errorf("relpath: %w", err)
	}
	res, err := runOneShot(ctx, b.Cli, b.Log, runOneShotOptions{
		Image:      "alpine",
		Network:    "",
		Entrypoint: nil,
		Cmd: []string{"sh", "-c", fmt.Sprintf("cd /snapshot && tar czf /dst/%s %s",
			filepath.Base(tarPath), filepath.ToSlash(rel))},
		Env: nil,
		Binds: []string{
			b.SnapshotDir + ":/snapshot:ro",
			b.DestDir + ":/dst",
		},
		Name: "",
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("tar fdb backup exited %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}
