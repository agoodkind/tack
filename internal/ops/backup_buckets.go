package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupBucketsInit idempotently creates the SeaweedFS S3 bucket that holds
// off-host backup artifacts. It is safe to run repeatedly; an already-existing
// bucket is a success, not an error. SeaweedFS only speaks path-style S3
// addressing, so the client forces UsePathStyle regardless of endpoint.
func RunBackupBucketsInit(ctx context.Context, cfg *config.Config) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" {
		err := fmt.Errorf("buckets-init: TACK_BACKUP_S3_ENDPOINT is required")
		logger.ErrorContext(ctx, "backup.buckets.failed", slog.String("err", err.Error()))
		return err
	}
	if cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf("buckets-init: TACK_BACKUP_S3_ACCESS_KEY_ID and TACK_BACKUP_S3_SECRET_ACCESS_KEY are required")
		logger.ErrorContext(ctx, "backup.buckets.failed", slog.String("err", err.Error()))
		return err
	}

	client := newBackupS3Client(cfg)
	buckets := []string{cfg.BackupS3BucketMain}

	logger.InfoContext(ctx, "backup.buckets.start",
		slog.String("endpoint", cfg.BackupS3Endpoint),
		slog.String("region", cfg.BackupS3Region),
		slog.Int("bucket_count", len(buckets)),
	)

	for _, bucket := range buckets {
		if err := ensureBucket(ctx, client, bucket); err != nil {
			wrapped := fmt.Errorf("buckets-init: ensure bucket %q: %w", bucket, err)
			logger.ErrorContext(ctx, "backup.buckets.failed",
				slog.String("bucket", bucket),
				slog.String("err", wrapped.Error()),
			)
			return wrapped
		}
	}

	logger.InfoContext(ctx, "backup.buckets.completed",
		slog.Int("bucket_count", len(buckets)),
	)
	return nil
}

// newBackupS3Client builds an S3 client for the SeaweedFS endpoint using static
// credentials from config. UsePathStyle is mandatory because SeaweedFS does not
// support virtual-hosted-style bucket addressing.
func newBackupS3Client(cfg *config.Config) *s3.Client {
	creds := credentials.NewStaticCredentialsProvider(
		cfg.BackupS3AccessKey,
		cfg.BackupS3SecretKey,
		"",
	)
	awsCfg := aws.Config{
		Region:      cfg.BackupS3Region,
		Credentials: creds,
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.BackupS3Endpoint)
		o.UsePathStyle = true
	})
}

// ensureBucket creates one bucket if it is missing. A successful HeadBucket
// means the bucket already exists and is reachable. A NotFound HeadBucket
// triggers CreateBucket, where BucketAlreadyOwnedByYou and BucketAlreadyExists
// are both treated as success to keep the operation idempotent under races.
func ensureBucket(ctx context.Context, client *s3.Client, bucket string) error {
	logger := telemetry.L(ctx)
	_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if headErr == nil {
		logger.InfoContext(ctx, "backup.bucket.exists", slog.String("bucket", bucket))
		return nil
	}
	if !isBucketNotFound(headErr) {
		wrapped := fmt.Errorf("head bucket %q: %w", bucket, headErr)
		logger.ErrorContext(ctx, "backup.bucket.head_failed",
			slog.String("bucket", bucket),
			slog.String("err", wrapped.Error()),
		)
		return wrapped
	}

	_, createErr := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if createErr != nil {
		if isBucketAlreadyOwned(createErr) {
			logger.InfoContext(ctx, "backup.bucket.exists", slog.String("bucket", bucket))
			return nil
		}
		wrapped := fmt.Errorf("create bucket %q: %w", bucket, createErr)
		logger.ErrorContext(ctx, "backup.bucket.create_failed",
			slog.String("bucket", bucket),
			slog.String("err", wrapped.Error()),
		)
		return wrapped
	}

	logger.InfoContext(ctx, "backup.bucket.created", slog.String("bucket", bucket))
	return nil
}

// isBucketNotFound reports whether a HeadBucket error means the bucket is
// absent. HeadBucket returns no typed body, so a NotFound smithy.APIError code
// or a NoSuchBucket typed error both indicate absence.
func isBucketNotFound(err error) bool {
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchBucket *s3types.NoSuchBucket
	if errors.As(err, &noSuchBucket) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NotFound" || code == "NoSuchBucket" || code == "404"
	}
	return false
}

// isBucketAlreadyOwned reports whether a CreateBucket error means the bucket
// already exists, which makes the create a no-op success.
func isBucketAlreadyOwned(err error) bool {
	var owned *s3types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return true
	}
	var exists *s3types.BucketAlreadyExists
	if errors.As(err, &exists) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "BucketAlreadyOwnedByYou" || code == "BucketAlreadyExists"
	}
	return false
}
