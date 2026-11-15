package ops

import (
	"context"
	"io"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

type stalenessEvidence struct {
	Bucket string `json:"bucket"`
	FDBLeg string `json:"fdb_leg"`
}

func (stalenessEvidence) isStepEvidence() {}

func runBackupStalenessCheckRecorded(
	ctx context.Context,
	cfg *config.Config,
	out io.Writer,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error, error) {
	if err := requireBackupStalenessConfig(ctx, cfg); err != nil {
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), nil, err
	}

	planned := stalenessEvidence{
		Bucket: cfg.BackupS3BucketMain,
		FDBLeg: "run",
	}
	if !cfg.BackupFDBContinuous {
		planned.FDBLeg = "skipped"
	}

	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), nil, err
	}

	var metrics []backupStalenessMetric
	var writeErr error
	step := audit.ExternalStep(
		externalTarget("s3/"+planned.Bucket+"/"+backupStatusPrefix),
		plannedJSON,
	)
	details, err := recordedStep(
		ctx,
		steps,
		step,
		func(ctx context.Context) error {
			metrics, writeErr = measureBackupStaleness(ctx, cfg, out)
			return nil
		},
	)
	if err != nil {
		return details, nil, err
	}

	return details, backupStalenessVerdict(ctx, metrics, writeErr), nil
}
