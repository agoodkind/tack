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
func RunBackupRestoreTest(ctx context.Context, cfg *config.Config, backupDir string) error {
	log := slog.Default()
	ctx, span := telemetry.StartSpan(ctx, "backup.restore_test")
	defer span.End()

	st, err := os.Stat(backupDir)
	if err != nil {
		log.ErrorContext(ctx, "backup.restore_test.stat_failed",
			slog.String("dir", backupDir),
			slog.Any("err", err),
		)
		return fmt.Errorf("stat %s: %w", backupDir, err)
	}
	if !st.IsDir() {
		notDirErr := fmt.Errorf("not a directory: %s", backupDir)
		log.ErrorContext(ctx, "backup.restore_test.not_a_dir",
			slog.String("dir", backupDir),
			slog.Any("err", notDirErr),
		)
		return notDirErr
	}

	err = assertDockerContext(ctx, cfg.BackupDockerContext)
	if err != nil {
		return err
	}

	cli, err := newDockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()

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

// listArtifacts returns the relative paths of all top-level files in
// backupDir that match the artifact filename patterns. The manifest itself
// is excluded.
func listArtifacts(backupDir string) ([]string, error) {
	dirEntries, err := os.ReadDir(backupDir)
	if err != nil {
		slog.Error("backup.restore_test.readdir_failed",
			slog.String("dir", backupDir),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("read dir %s: %w", backupDir, err)
	}
	var out []string
	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == manifestFileName {
			continue
		}
		if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".sql.gz") {
			out = append(out, name)
		}
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
