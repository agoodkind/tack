// The recovery drill restores engine backups into temporary containers,
// checks the restored data, and removes the scratch resources. It exercises
// the recovery procedure against real backups rather than checking only
// artifact structure. Audit-ledger checks verify the hash chain as well as
// the restored rows.

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

// restoreDrillCtx groups per-run state. RunID separates scratch containers
// and directories from the live stack and parallel drills. Tracked resource
// identifiers permit cleanup after a partial failure or interruption.
type restoreDrillCtx struct {
	Cfg   *config.Config
	Cli   *client.Client
	RunID string
	// YBPass stores the temporary YSQL password for scratch yugabyted. Each run
	// derives its password rather than storing a credential literal in source.
	// Teardown destroys the scratch container; this password does not grant
	// access to the live system.
	YBPass string
	// YBRunKey selects the YugabyteDB export run for --yb-run-key. An empty value
	// selects the newest complete run. An explicit key must identify a complete
	// run.
	YBRunKey string
	// FDBTargetTime selects the FoundationDB restore moment for
	// --fdb-target-time. Nil selects the latest restorable point. Every non-nil
	// value represents an explicit target.
	FDBTargetTime  *time.Time
	containerNames []string
	// scratchImage selects the engine image for removing the run's scratch
	// directory. An empty value means an engine has not used that directory.
	scratchImage string
	// runLock retains the scratch-directory lock that claimDrillRun acquires.
	runLock *os.File
}

func (r *restoreDrillCtx) trackContainer(name string) {
	r.containerNames = append(r.containerNames, name)
}

// cleanupRestoreDrill force-removes scratch containers before attempting
// scratch-directory removal and releases the run lock last. Container
// removal waits for Docker confirmation before directory cleanup. The lock
// prevents a sweep from removing the directory during teardown.
//
// The cleanup context ignores parent cancellation, including SIGINT.
// Scratch-directory removal makes a best-effort attempt.
func cleanupRestoreDrill(ctx context.Context, r *restoreDrillCtx) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	for _, name := range r.containerNames {
		awaitBackupContainerRemoval(bg, r.Cli, name)
	}
	removeDrillScratch(ctx, r)
	releaseDrillRun(r)
}

// RestoreDrillOptions specifies the operator's target for each restore leg.
// Its zero value uses the default targets.
type RestoreDrillOptions struct {
	// YBRunKey selects one YugabyteDB export run, which must be complete.
	// An empty value selects the newest complete run.
	YBRunKey string
	// FDBTargetTime selects a FoundationDB restore moment within the backup's
	// restorable window. Nil selects the latest restorable point. A pointer
	// preserves every explicit target, including the zero time, for validation
	// against the window.
	FDBTargetTime *time.Time
}

// RunBackupRestoreDrill restores the FoundationDB continuous backup and the
// YugabyteDB distributed-snapshot export into temporary containers. It checks
// data in each restored store and verifies the audit ledger's hash chain.
//
// Each enabled leg runs even if another leg fails. The result reports every
// failed leg. RestoreDrillOptions can select one YugabyteDB export run and one
// FoundationDB target moment. The drill first removes killed runs' resources.
//
// After the restore legs pass, the drill records a rehearsal marker:
//   - The marker dates the drill for ops backup staleness-check.
//   - A marker-write failure fails the drill without rerunning restore legs.
//   - The marker writer uses its own retry policy.
func RunBackupRestoreDrill(ctx context.Context, cfg *config.Config, opts RestoreDrillOptions) error {
	lock, lockErr := lockBackupOperation(ctx, cfg.BackupRoot)
	if lockErr != nil {
		return lockErr
	}
	defer lock.Close()
	ctx = withBackupOperationLock(ctx)
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
		return err
	}
	logger.InfoContext(ctx, "backup.restore_drill.ok", slog.String("run_id", rctx.RunID))
	return nil
}

// devNull discards writes when the caller does not need an exec's standard
// output.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
