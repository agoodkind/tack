package ops

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// continuousDisabledCode is the error code of fdb-continuous-init when
// TACK_BACKUP_FDB_CONTINUOUS is false.
const continuousDisabledCode = "continuous_backup_disabled"

// continuousBackupEvidence is the evidence of the FoundationDB continuous
// backup step: the destination bucket and endpoint host and the snapshot
// interval. It omits the blobstore credentials.
type continuousBackupEvidence struct {
	Bucket                  string `json:"bucket"`
	EndpointHost            string `json:"endpoint_host"`
	SnapshotIntervalSeconds int    `json:"snapshot_interval_seconds"`
}

func (continuousBackupEvidence) isStepEvidence() {}

// snapshotScheduleEvidence is the evidence of the YugabyteDB point-in-time
// recovery schedule step.
type snapshotScheduleEvidence struct {
	Filter           string `json:"filter"`
	IntervalMinutes  int    `json:"interval_minutes"`
	RetentionMinutes int    `json:"retention_minutes"`
}

func (snapshotScheduleEvidence) isStepEvidence() {}

// runBackupFDBContinuousInitRecorded runs fdb-continuous-init as one step.
// It refuses to start when TACK_BACKUP_FDB_CONTINUOUS is false.
func runBackupFDBContinuousInitRecorded(ctx context.Context, cfg *config.Config, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if !cfg.BackupFDBContinuous {
		err := RunBackupFDBContinuousInit(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, continuousDisabledCode, err), err
	}
	evidence, err := encodeEvidence(ctx, continuousBackupEvidence{
		Bucket: cfg.BackupS3BucketMain, EndpointHost: endpointHost(cfg.BackupS3Endpoint),
		SnapshotIntervalSeconds: cfg.BackupFDBSnapshotInterval,
	})
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	step := audit.ExternalStep(externalTarget("fdb/continuous-backup/"+cfg.BackupS3BucketMain), evidence)
	return recordedStep(ctx, steps, step,
		func(ctx context.Context) error { return RunBackupFDBContinuousInit(ctx, cfg) },
		func(context.Context) (json.RawMessage, error) { return evidence, nil })
}

// runBackupYBPITRInitRecorded runs yb-pitr-init as one step for the snapshot
// schedule of the configured database.
func runBackupYBPITRInitRecorded(ctx context.Context, cfg *config.Config, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if cfg.BackupYBImage == "" {
		err := RunBackupYBPITRInit(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	filter := "ysql." + cfg.YugabyteDB
	evidence, err := encodeEvidence(ctx, snapshotScheduleEvidence{
		Filter: filter, IntervalMinutes: cfg.BackupYBPITRIntervalMinutes, RetentionMinutes: cfg.BackupYBPITRRetentionMinutes,
	})
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	step := audit.ExternalStep(externalTarget("yugabyte/snapshot-schedule/"+filter), evidence)
	return recordedStep(ctx, steps, step,
		func(ctx context.Context) error { return RunBackupYBPITRInit(ctx, cfg) },
		func(context.Context) (json.RawMessage, error) { return evidence, nil })
}
