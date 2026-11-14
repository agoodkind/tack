// Recovery drills restore real backups into scratch containers and verify
// restored data before removing resources. Ledger verification includes hash
// chains rather than only checking that rows exist.

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

// restoreDrillCtx stores one run's state. Unique IDs isolate scratch resources
// from the live stack and concurrent drills. Tracked containers permit cleanup
// after partial failure or interruption.
type restoreDrillCtx struct {
	Cfg   *config.Config
	Cli   *client.Client
	RunID string
	// YBPass derives a temporary password per scratch run without a literal
	// credential in source. Teardown destroys the database that accepts it.
	YBPass string
	// YBRunKey selects an explicit export and rejects an incomplete run.
	// Empty values select the newest complete export.
	YBRunKey string
	// FDBTargetTime preserves explicit targeting, including the zero time.
	// Nil selects the latest restorable point.
	FDBTargetTime  *time.Time
	containerNames []string
	// scratchImage selects the engine image for deleting its scratch directory.
	// An empty value indicates that an engine has not written there.
	scratchImage string
	// runLock locks this run's scratch directory until cleanup finishes.
	runLock *os.File
}

func (r *restoreDrillCtx) trackContainer(name string) {
	r.containerNames = append(r.containerNames, name)
}

// cleanupRestoreDrill removes containers before directories to stop engine
// writes, and releases the lock last to exclude concurrent orphan sweeps.
// A context independent of parent cancellation permits teardown after SIGINT.
func cleanupRestoreDrill(ctx context.Context, r *restoreDrillCtx) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()

	for _, name := range r.containerNames {
		awaitBackupContainerRemoval(bg, r.Cli, name)
	}

	removeDrillScratch(ctx, r)
	releaseDrillRun(r)
}

// RestoreDrillOptions configures per-engine targets. Zero values select the
// latest restorable FoundationDB point and newest complete SQL export.
type RestoreDrillOptions struct {
	// YBRunKey selects an explicit export, rejecting incomplete runs.
	// Empty values select the newest complete run.
	YBRunKey string
	// FDBTargetTime selects a moment within a backup's restorable window.
	// The pointer distinguishes explicit zero time from an absent target.
	FDBTargetTime *time.Time
}

// RunBackupRestoreDrill restores both enabled engines and verifies data,
// including the SQL audit hash chain. Each leg runs despite other leg failures;
// the final error lists every failed leg. Targets can select one SQL export
// and a FoundationDB moment. The run first removes abandoned resources.
//
// Passing legs require a dated rehearsal marker for staleness checks. Marker
// writes retry independently and can fail the drill without repeating restores.
func RunBackupRestoreDrill(
	ctx context.Context,
	cfg *config.Config,
	opts RestoreDrillOptions,
) error {
	lock, lockErr := lockBackupOperation(ctx, cfg.BackupRoot)
	if lockErr != nil {
		return lockErr
	}
	defer lock.Close()
	ctx = withBackupOperationLock(ctx)
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf(
			"restore-drill: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required",
		)
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
		YBRunKey:       opts.YBRunKey,
		FDBTargetTime:  opts.FDBTargetTime,
		containerNames: nil,
		scratchImage:   "",
		runLock:        nil,
	}
	defer cleanupRestoreDrill(ctx, rctx)
	if err := claimDrillRun(ctx, rctx); err != nil {
		return err
	}

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
		logger.WarnContext(
			ctx,
			"backup.restore_drill.leg_skipped",
			slog.String("leg", "fdb"),
			slog.String("reason", "TACK_BACKUP_FDB_CONTINUOUS is false"),
		)
	}

	if legErr := restoreDrillYugabyte(ctx, rctx); legErr != nil {
		failures = append(failures, "yugabyte: "+legErr.Error())
	} else {
		passed = append(passed, "yugabyte")
		logger.InfoContext(ctx, "backup.restore_drill.leg_ok", slog.String("leg", "yugabyte"))
	}

	if len(failures) > 0 {
		err := fmt.Errorf(
			"restore-drill: %d leg(s) failed: %s",
			len(failures),
			strings.Join(failures, "; "),
		)
		logger.ErrorContext(ctx, "backup.restore_drill.failed", slog.String("err", err.Error()))

		return err
	}

	s3Client := newBackupS3Client(cfg)
	markerPut := func(key string, body []byte) error {
		return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
	}
	if err := recordRestoreDrillRehearsal(ctx, markerPut, rctx.RunID, passed); err != nil {
		return err
	}

	logger.InfoContext(ctx, "backup.restore_drill.ok", slog.String("run_id", rctx.RunID))

	return nil
}

// devNull discards irrelevant container stdout.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
