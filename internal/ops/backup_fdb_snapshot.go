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
