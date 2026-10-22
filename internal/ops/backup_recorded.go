package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

const (
	// bucketCreated marks a bucket that did not exist before buckets-init.
	bucketCreated = "created"
	// bucketPresent is the observed state of a bucket that existed before
	// buckets-init.
	bucketPresent = "already_present"
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

// bucketEvidence is the evidence of one buckets-init step.
type bucketEvidence struct {
	Bucket       string `json:"bucket"`
	EndpointHost string `json:"endpoint_host"`
	State        string `json:"state,omitempty"`
}

func (bucketEvidence) isStepEvidence() {}

// runBackupBucketsInitRecorded runs buckets-init as one step for the backup
// bucket. It records whether the bucket existed before the step.
func runBackupBucketsInitRecorded(ctx context.Context, cfg *config.Config, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := RunBackupBucketsInit(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	client := newBackupS3Client(cfg)
	bucket := cfg.BackupS3BucketMain
	_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if headErr != nil && !isBucketNotFound(headErr) {
		slog.ErrorContext(ctx, "backup.bucket.head_failed", slog.String("bucket", bucket), slog.String("err", headErr.Error()))
		wrapped := fmt.Errorf("buckets-init: read bucket %q: %w", bucket, headErr)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	state := bucketCreated
	if headErr == nil {
		state = bucketPresent
	}
	planned := bucketEvidence{Bucket: bucket, EndpointHost: endpointHost(cfg.BackupS3Endpoint), State: ""}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	step := audit.ExternalStep(externalTarget("s3/"+bucket), plannedJSON)
	return recordedStep(ctx, steps, step,
		func(ctx context.Context) error { return RunBackupBucketsInit(ctx, cfg) },
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.State = state
			return encodeEvidence(ctx, observed)
		})
}
