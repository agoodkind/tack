package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupCtx bundles the runtime state for one backup run. Component
// dumpers receive a pointer so they share the same destination directory,
// docker client, and logger without any package-level state.
type backupCtx struct {
	Cfg     *config.Config
	Cli     *client.Client
	Log     *slog.Logger
	RunID   string
	DestDir string
}

// runBackupRun is the top-level orchestrator for `./server ops backup`.
// Components run in sequence: FDB ensures its continuous session first, then
// SQL dumps and Meilisearch write local artifacts. The manifest is the last
// step.
func runBackupRun(ctx context.Context, cfg *config.Config) error {
	log := slog.Default()
	if cfg.BackupTemporalDBPass == "" {
		missingPWErr := fmt.Errorf("TACK_BACKUP_TEMPORAL_DB_PASSWORD is required; refuse to ship a partial snapshot")
		log.ErrorContext(ctx, "backup.run.missing_temporal_pw", "err", missingPWErr)
		return missingPWErr
	}
	if cfg.YugabytePassword == "" {
		missingPWErr := fmt.Errorf("YUGABYTE_PASSWORD is required; refuse to ship a partial snapshot")
		log.ErrorContext(ctx, "backup.run.missing_yb_pw", "err", missingPWErr)
		return missingPWErr
	}

	// SDK reads DOCKER_CONTEXT/DOCKER_HOST via client.FromEnv. The earlier
	// shell-out to `docker context inspect` was removed per TACK-264.

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	bctx, err := newBackupCtx(ctx, cfg, cli, log)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "backup.run.started",
		slog.String("run_id", bctx.RunID),
		slog.String("dest", bctx.DestDir),
	)

	steps := []struct {
		name string
		fn   func(context.Context, *backupCtx) error
	}{
		{"fdb", runBackupFDB},
		{"yugabyte", runBackupYugabyte},
		{"temporal_db", runBackupTemporalDB},
		{"meilisearch", runBackupMeilisearch},
	}
	for _, s := range steps {
		stepCtx, span := telemetry.StartSpan(ctx, "backup.step."+s.name)
		err := s.fn(stepCtx, bctx)
		span.End()
		if err != nil {
			log.ErrorContext(ctx, "backup.step.failed",
				slog.String("step", s.name),
				slog.Any("err", err),
			)
			return fmt.Errorf("backup step %s: %w", s.name, err)
		}
		log.DebugContext(ctx, "backup.step.complete", slog.String("step", s.name))
	}

	entries, err := writeManifest(ctx, bctx.Log, bctx.DestDir)
	if err != nil {
		log.ErrorContext(ctx, "backup.manifest.failed", slog.Any("err", err))
		return fmt.Errorf("write manifest: %w", err)
	}
	log.InfoContext(ctx, "backup.manifest.written",
		slog.String("dest", bctx.DestDir),
		slog.Int("entries", len(entries)),
	)

	err = updateLatestPointer(ctx, log, cfg.BackupRoot, bctx.RunID)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "backup.run.completed",
		slog.String("run_id", bctx.RunID),
		slog.String("dest", bctx.DestDir),
		slog.Int("artifacts", len(entries)),
	)
	return nil
}

// newBackupCtx allocates the local artifact directory for one backup run.
func newBackupCtx(ctx context.Context, cfg *config.Config, cli *client.Client, log *slog.Logger) (*backupCtx, error) {
	runID := opsNow().UTC().Format("20060102T150405Z")
	destDir := filepath.Join(cfg.BackupRoot, "tack-"+runID)

	err := os.MkdirAll(destDir, 0o750)
	if err != nil {
		log.ErrorContext(ctx, "backup.mkdir.dest_failed",
			slog.String("dest", destDir),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("mkdir dest %s: %w", destDir, err)
	}
	return &backupCtx{
		Cfg:     cfg,
		Cli:     cli,
		Log:     log,
		RunID:   runID,
		DestDir: destDir,
	}, nil
}

// updateLatestPointer writes /root/backups/.latest with the run ID so
// downstream tools can find the most recent run without scanning the
// filesystem.
func updateLatestPointer(ctx context.Context, log *slog.Logger, backupRoot, runID string) error {
	latest := filepath.Join(backupRoot, ".latest")
	err := os.WriteFile(latest, []byte(runID+"\n"), 0o600)
	if err != nil {
		log.ErrorContext(ctx, "backup.latest_pointer.failed",
			slog.String("path", latest),
			slog.Any("err", err),
		)
		return fmt.Errorf("write %s: %w", latest, err)
	}
	log.InfoContext(ctx, "backup.latest_pointer.updated",
		slog.String("path", latest),
		slog.String("run_id", runID),
	)
	return nil
}
