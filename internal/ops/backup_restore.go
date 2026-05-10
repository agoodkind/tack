package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// restoreCtx bundles per-run state for the restore-test family. The unique
// runID prevents two parallel restore-tests from clobbering each other's
// scratch resources.
type restoreCtx struct {
	Cfg       *config.Config
	Cli       *client.Client
	Log       *slog.Logger
	BackupDir string
	RunID     string
	// containerNames and volumeNames are tracked so the deferred cleanup
	// can tear them down even on partial failure.
	containerNames []string
	volumeNames    []string
}

// dockerClient returns the embedded client; kept as a method so future
// callers can swap in a fake client without touching field-access call
// sites.
func (r *restoreCtx) dockerClient() *client.Client { return r.Cli }

// RunBackupRestoreTest is the entry point for
// `./server ops backup restore-test <path>`. It iterates the artifacts
// in the backup dir and replays each into a scratch container; success
// is the conjunction of every per-artifact replay reporting healthy.
// The path must be local: either run the binary on CT 117 where the backup
// lives, or pull the backup to a local path first and run there.
func RunBackupRestoreTest(ctx context.Context, cfg *config.Config, backupDir string) error {
	log := slog.Default()
	ctx, span := telemetry.StartSpan(ctx, "backup.restore_test")
	defer span.End()

	// SDK reads DOCKER_CONTEXT/DOCKER_HOST via client.FromEnv. The earlier
	// shell-out to `docker context inspect` was removed per TACK-264.

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	if err = checkBackupDirOnDaemon(ctx, cli, log, backupDir); err != nil {
		return err
	}

	rctx := &restoreCtx{
		Cfg:            cfg,
		Cli:            cli,
		Log:            log,
		BackupDir:      backupDir,
		RunID:          fmt.Sprintf("rt%s-%d", opsNow().UTC().Format("20060102T150405Z"), os.Getpid()),
		containerNames: nil,
		volumeNames:    nil,
	}
	defer cleanupRestoreCtx(ctx, rctx)

	log.InfoContext(ctx, "backup.restore_test.started",
		slog.String("dir", backupDir),
		slog.String("run_id", rctx.RunID),
	)

	entries, err := listArtifacts(backupDir)
	if err != nil {
		return err
	}

	var failures []string
	for _, art := range entries {
		cat := categoryFromName(art)
		artPath := filepath.Join(backupDir, art)
		var stepErr error
		switch cat {
		case categoryFDB:
			stepErr = restoreFDBArtifact(ctx, rctx, artPath)
		case categoryPgDumpYugabyte:
			stepErr = restorePgDump(ctx, rctx, artPath, pgRoleYugabyte)
		case categoryPgDumpTemporal:
			stepErr = restorePgDump(ctx, rctx, artPath, pgRoleTemporal)
		case categoryMeilisearch:
			stepErr = restoreMeilisearch(ctx, rctx, artPath)
		case categoryYugabyteTar:
			log.WarnContext(ctx, "backup.restore_test.skip_volume_tar",
				slog.String("artifact", art))
		case categoryUnknown:
			continue
		}
		if stepErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", art, stepErr))
			log.ErrorContext(ctx, "backup.restore_test.artifact_failed",
				slog.String("artifact", art),
				slog.Any("err", stepErr),
			)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("backup restore-test: %d failure(s): %s",
			len(failures), strings.Join(failures, "; "))
	}
	log.InfoContext(ctx, "backup.restore_test.ok",
		slog.Int("artifacts", len(entries)),
	)
	return nil
}

// checkBackupDirOnDaemon verifies that dir exists as a non-empty directory on
// the Docker daemon's host. It runs a one-shot alpine container that executes
// "ls -la <dir>" with the path bind-mounted, then captures the exit status.
// When the binary runs with DOCKER_CONTEXT=tack the daemon is on CT 117, so
// a local [os.Stat] would always fail; this check runs entirely daemon-side
// and works in both the local and remote daemon cases.
//
// The container is created via the SDK's ContainerCreate using the same
// daemon connection [client.FromEnv] resolves; bind-mount paths are
// interpreted on the daemon's host. Docker Desktop's macOS file-sharing
// allowlist applies only to local paths, not to paths sent over a remote
// daemon connection (e.g. ssh://), so the SDK works in both local and
// remote daemon cases.
func checkBackupDirOnDaemon(ctx context.Context, cli *client.Client, log *slog.Logger, dir string) error {
	res, err := runOneShot(ctx, cli, log, runOneShotOptions{
		Image:      "alpine",
		Network:    "",
		Entrypoint: nil,
		Cmd:        []string{"ls", "-la", "/check"},
		Env:        nil,
		Binds:      []string{dir + ":/check:ro"},
		Name:       "",
	})
	if err != nil {
		log.ErrorContext(ctx, "backup.restore_test.path_check_failed",
			slog.String("dir", dir),
			slog.Any("err", err),
		)
		return fmt.Errorf("path check for %s: %w", dir, err)
	}
	if res.ExitCode != 0 {
		pathErr := fmt.Errorf("path check for %s exited %d: %s", dir, res.ExitCode, res.Stderr)
		log.ErrorContext(ctx, "backup.restore_test.path_check_failed",
			slog.String("dir", dir),
			slog.Any("err", pathErr),
		)
		return pathErr
	}
	log.InfoContext(ctx, "backup.restore_test.path_check_ok", slog.String("dir", dir))
	return nil
}

// listArtifacts returns the basenames of restorable artifact files at the top
// level of backupDir. Today's `runBackupRun` writes each component as a
// top-level file (fdb-snapshot.tar.gz, yugabyte.sql, temporal-db.sql,
// tack_meili-data.tar.gz). MANIFEST.txt is skipped. Subdirectories are
// skipped: the only one we create today is `fdb/` for metadata files
// (describe.txt) that are not themselves restorable. Classification of each
// returned name is done by the caller via [categoryFromName]; files that do
// not match any category fall through to the unknown branch and are skipped.
func listArtifacts(backupDir string) ([]string, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		slog.Error("backup.restore_test.readdir_failed",
			slog.String("dir", backupDir),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("read backup dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == manifestFileName {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// trackContainer records a container name so deferred cleanup will remove
// it.
func (r *restoreCtx) trackContainer(name string) {
	r.containerNames = append(r.containerNames, name)
}

// trackVolume records a volume name so deferred cleanup will remove it.
func (r *restoreCtx) trackVolume(name string) {
	r.volumeNames = append(r.volumeNames, name)
}

// cleanupRestoreCtx tears down every scratch container and volume on best
// effort. The function uses a derived background-shaped context so it
// still runs when the parent context has been cancelled by a signal.
func cleanupRestoreCtx(ctx context.Context, r *restoreCtx) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	cli := r.dockerClient()
	for _, name := range r.containerNames {
		removeContainerForce(bg, cli, name)
	}
	for _, vol := range r.volumeNames {
		_, _ = cli.VolumeRemove(bg, vol, client.VolumeRemoveOptions{Force: true})
	}
}
