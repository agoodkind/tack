package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/telemetry"
)

// putObjectFromFile uploads a local file to bucket under key. The file is
// streamed rather than read into memory, and [os.File] is seekable so the S3
// client can set Content-Length and retry the body. Reuses the client built by
// newBackupS3Client; used by the backup family to land artifacts in the
// SeaweedFS object store.
func putObjectFromFile(ctx context.Context, client *s3.Client, bucket, key, path string) error {
	logger := telemetry.L(ctx)
	f, err := os.Open(path)
	if err != nil {
		wrapped := fmt.Errorf("open %s for upload to %s/%s: %w", path, bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.put_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		wrapped := fmt.Errorf("stat %s for upload to %s/%s: %w", path, bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.put_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(info.Size()),
	})
	if err != nil {
		wrapped := fmt.Errorf("put object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.put_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}

	logger.InfoContext(
		ctx, "backup.s3.put",
		slog.String("bucket", bucket),
		slog.String("key", key),
		slog.Int64("bytes", info.Size()),
	)
	return nil
}
