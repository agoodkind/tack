package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

const defaultBackupRetentionDays = 14

// BackupRetentionOptions selects the number of days of restorable backups.
type BackupRetentionOptions struct {
	KeepDays int `json:"keep_days"`
}

type backupRetentionPlan struct {
	Cutoff     time.Time            `json:"cutoff"`
	LedgerRuns []string             `json:"ledger_runs"`
	FDB        []retentionFDBAction `json:"foundationdb"`
}

func retentionCutoff(options BackupRetentionOptions) (time.Time, error) {
	if options.KeepDays <= 0 {
		return time.Time{}, errors.New("backup retention requires a positive --keep-days value")
	}
	return opsNow().UTC().Truncate(time.Second).AddDate(0, 0, -options.KeepDays), nil
}

func planBackupRetention(ctx context.Context, cfg *config.Config, options BackupRetentionOptions) (backupRetentionPlan, error) {
	cutoff, err := retentionCutoff(options)
	plan := backupRetentionPlan{Cutoff: cutoff, LedgerRuns: nil, FDB: nil}
	if err != nil {
		return plan, err
	}
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" || cfg.BackupS3BucketMain == "" {
		return plan, errors.New("backup retention requires the S3 endpoint, bucket, access key, and secret key")
	}
	plan.LedgerRuns, err = planRetentionLedger(ctx, cfg, newBackupS3Client(cfg), cutoff)
	if err != nil {
		return plan, err
	}
	if cfg.BackupFDBContinuous {
		plan.FDB, err = planRetentionFDB(ctx, cfg, cutoff)
	}
	return plan, err
}

// PreviewBackupRetention reports the same selection as execution without
// deleting objects or expiring a FoundationDB session.
func PreviewBackupRetention(ctx context.Context, cfg *config.Config, options BackupRetentionOptions, output io.Writer) error {
	lock, err := lockBackupOperation(ctx, cfg.BackupRoot)
	if err != nil {
		return err
	}
	defer lock.Close()
	ctx = withBackupOperationLock(ctx)
	plan, err := planBackupRetention(ctx, cfg, options)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(plan); err != nil {
		slog.ErrorContext(ctx, "backup.retention.preview_write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write retention preview: %w", err)
	}
	return nil
}

// RunBackupRetention removes selected old backups and records each target's
// observed result. The caller must use the external-operation audit gate.
func RunBackupRetention(ctx context.Context, cfg *config.Config, options BackupRetentionOptions, output io.Writer, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if steps == nil {
		err := errors.New("backup retention requires an audit step recorder")
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	lock, err := lockBackupOperation(ctx, cfg.BackupRoot)
	if err != nil {
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	defer lock.Close()
	ctx = withBackupOperationLock(ctx)
	plan, err := planBackupRetention(ctx, cfg, options)
	if err != nil {
		slog.ErrorContext(ctx, "backup.retention.refused", slog.String("err", err.Error()))
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	if err := json.NewEncoder(output).Encode(plan); err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	completed := 0
	for _, run := range plan.LedgerRuns {
		evidence := retentionEvidence{Bucket: cfg.BackupS3BucketMain, Target: ybSnapshotKeyPrefix(run), Action: "delete", Cutoff: plan.Cutoff, DeletedObjects: 0, RequestedObjects: 0, Uncertain: false, ExpireBeforeVersion: 0}
		err := retentionStep(ctx, steps, evidence, func() (int, error) {
			return deleteRetentionPrefix(ctx, newBackupS3Client(cfg), cfg.BackupS3BucketMain, run)
		})
		if err != nil {
			return retentionFailure(completed, err)
		}
		completed++
	}
	for _, action := range plan.FDB {
		evidence := retentionEvidence{Bucket: cfg.BackupS3BucketMain, Target: "foundationdb/" + action.Name, Action: action.Action, Cutoff: plan.Cutoff, DeletedObjects: 0, RequestedObjects: 0, Uncertain: false, ExpireBeforeVersion: action.ExpireBeforeVersion}
		err := retentionStep(ctx, steps, evidence, func() (int, error) {
			return 0, applyRetentionFDB(ctx, cfg, plan.Cutoff, action)
		})
		if err != nil {
			return retentionFailure(completed, err)
		}
		completed++
	}
	slog.InfoContext(ctx, "backup.retention.completed", slog.Time("cutoff", plan.Cutoff), slog.Int("targets", completed))
	return commandOutcome(audit.OutcomeCompleted, "", nil), nil
}

func retentionFailure(completed int, err error) (audit.OutcomeDetails, error) {
	outcome := audit.OutcomeFailed
	var deletionError *retentionDeleteError
	if errors.As(err, &deletionError) && deletionError.uncertain {
		outcome = audit.OutcomeIncomplete
	}
	var partial *retentionPartialError
	if completed > 0 || errors.As(err, &partial) {
		outcome = audit.OutcomePartiallyCompleted
	}
	slog.Error("backup.retention.failed", slog.String("err", err.Error()), slog.Int("completed_targets", completed))
	return commandOutcome(outcome, commandFailedCode, err), fmt.Errorf("backup retention: %w", err)
}
