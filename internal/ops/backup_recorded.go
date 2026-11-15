package ops

import (
	"context"
	"net/url"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// endpointHost returns the host and port of the object-store endpoint
// without any user information.
func endpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}

	return parsed.Host
}

type bucketEvidence struct {
	Bucket       string `json:"bucket"`
	EndpointHost string `json:"endpoint_host"`
}

func (bucketEvidence) isStepEvidence() {}

// runBackupBucketsInitRecorded records the selected bucket before creation.
func runBackupBucketsInitRecorded(
	ctx context.Context,
	cfg *config.Config,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := RunBackupBucketsInit(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}

	bucket := cfg.BackupS3BucketMain
	planned := bucketEvidence{
		Bucket:       bucket,
		EndpointHost: endpointHost(cfg.BackupS3Endpoint),
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	step := audit.ExternalStep(externalTarget("s3/"+bucket), plannedJSON)

	return recordedStep(
		ctx,
		steps,
		step,
		func(ctx context.Context) error { return RunBackupBucketsInit(ctx, cfg) },
	)
}
