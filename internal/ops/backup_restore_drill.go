// backup_restore_drill.go runs the recovery drill: it restores each engine's
// backup into a throwaway container, asserts the data is present, and tears the
// scratch resources down. It proves the documented recovery procedure works
// against real backups, rather than that an artifact is merely well-formed.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// restoreDrillCtx bundles per-run state. The unique RunID namespaces every
// scratch container and volume so a drill never collides with the live stack or
// a parallel drill, and the tracked names let teardown remove them even on
// partial failure or interrupt.
type restoreDrillCtx struct {
	Cfg   *config.Config
	Cli   *client.Client
	RunID string
	// YBPass is the throwaway YSQL password for the scratch yugabyted,
	// derived per run so no credential literal lives in source. The scratch
	// container is destroyed at teardown, so it is not a real secret.
	YBPass string
	// YBRunKey is the yugabyte export run the operator explicitly asked to
	// drill (--yb-run-key). Empty means discover the newest complete run; an
	// explicit key is refused if that run is incomplete.
	YBRunKey       string
	containerNames []string
	volumeNames    []string
}

func (r *restoreDrillCtx) trackContainer(name string) {
	r.containerNames = append(r.containerNames, name)
}
func (r *restoreDrillCtx) trackVolume(name string) { r.volumeNames = append(r.volumeNames, name) }

// cleanupRestoreDrill force-removes every scratch container and volume on best
// effort. It derives a non-cancellable context so teardown still runs after a
// SIGINT cancels the parent.
func cleanupRestoreDrill(ctx context.Context, r *restoreDrillCtx) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	for _, name := range r.containerNames {
		removeContainerForce(bg, r.Cli, name)
	}
	for _, vol := range r.volumeNames {
		_, _ = r.Cli.VolumeRemove(bg, vol, client.VolumeRemoveOptions{Force: true})
	}
}

// RunBackupRestoreDrill restores FoundationDB (from the continuous backup) and
// YugabyteDB (from the distributed-snapshot export) into throwaway containers
// and asserts each holds data. Meilisearch is excluded because it rebuilds from
// FoundationDB, and Temporal is excluded because it holds no Tack data. Each leg
// runs even if another fails so the drill reports a complete picture; the drill
// errors if any attempted leg fails. ybRunKey optionally pins the yugabyte leg
// to one export run; empty means the newest complete run. A drill where every
// attempted leg passed records a rehearsal marker in the object store, which is
// what `ops backup staleness-check` dates the rehearsal from.
func RunBackupRestoreDrill(ctx context.Context, cfg *config.Config, ybRunKey string) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("restore-drill: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.restore_drill.failed", slog.String("err", err.Error()))
		return err
	}

	cli, err := newDockerClient(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()

	runID := fmt.Sprintf("rt%s-%d", opsNow().UTC().Format("20060102T150405Z"), os.Getpid())
	rctx := &restoreDrillCtx{
		Cfg:            cfg,
		Cli:            cli,
		RunID:          runID,
		YBPass:         "drill-" + runID,
		YBRunKey:       ybRunKey,
		containerNames: nil,
		volumeNames:    nil,
	}
	defer cleanupRestoreDrill(ctx, rctx)

	logger.InfoContext(ctx, "backup.restore_drill.started", slog.String("run_id", rctx.RunID))

	var failures []string
	var passed []string

	if cfg.BackupFDBContinuous {
		if legErr := restoreDrillFDB(ctx, rctx); legErr != nil {
			failures = append(failures, "fdb: "+legErr.Error())
		} else {
			passed = append(passed, "fdb")
			logger.InfoContext(ctx, "backup.restore_drill.leg_ok", slog.String("leg", "fdb"))
		}
	} else {
		logger.WarnContext(ctx, "backup.restore_drill.leg_skipped",
			slog.String("leg", "fdb"),
			slog.String("reason", "TACK_BACKUP_FDB_CONTINUOUS is false"))
	}

	if legErr := restoreDrillYugabyte(ctx, rctx); legErr != nil {
		failures = append(failures, "yugabyte: "+legErr.Error())
	} else {
		passed = append(passed, "yugabyte")
		logger.InfoContext(ctx, "backup.restore_drill.leg_ok", slog.String("leg", "yugabyte"))
	}

	if len(failures) > 0 {
		err := fmt.Errorf("restore-drill: %d leg(s) failed: %s", len(failures), strings.Join(failures, "; "))
		logger.ErrorContext(ctx, "backup.restore_drill.failed", slog.String("err", err.Error()))
		return err
	}
	s3Client := newBackupS3Client(cfg)
	markerPut := func(key string, body []byte) error {
		return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
	}
	if err := recordRestoreDrillRehearsal(ctx, markerPut, rctx.RunID, passed); err != nil {
		wrapped := fmt.Errorf("restore-drill: every leg passed but the rehearsal marker did not land: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.ok", slog.String("run_id", rctx.RunID))
	return nil
}

// recordRestoreDrillRehearsal records a passing drill so the staleness check
// can tell how long ago recovery was last rehearsed. The marker is part of the
// drill's success, not a side effect: a drill nobody can date is
// indistinguishable from a drill that never ran, which is the failure the
// staleness alert exists to catch. put is bound to the object store by the
// caller, so what the drill records stays checkable against what the staleness
// check reads.
func recordRestoreDrillRehearsal(
	ctx context.Context,
	put func(key string, body []byte) error,
	runID string,
	legs []string,
) error {
	return writeBackupStatusMarker(ctx, put, backupStalenessRehearsalName, opsNow().UTC(),
		"restore drill "+runID+" passed: "+strings.Join(legs, ", "))
}

// waitExecOK polls a check command inside a container until it exits zero or the
// timeout elapses, returning an error on timeout. Used to wait for a scratch
// engine to become ready.
func waitExecOK(ctx context.Context, r *restoreDrillCtx, container string, timeout time.Duration, env []string, cmd []string) error {
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		exitCode, _, err := containerExecStreaming(pollCtx, r.Cli, container, cmd, env, devNull{})
		if err == nil && exitCode == 0 {
			return nil
		}
		select {
		case <-pollCtx.Done():
			err := fmt.Errorf("readiness check %v timed out after %s: %w", cmd, timeout, pollCtx.Err())
			telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.wait_timeout",
				slog.String("container", container), slog.String("err", err.Error()))
			return err
		case <-ticker.C:
		}
	}
}

// devNull discards writes; used when an exec's stdout is irrelevant.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
