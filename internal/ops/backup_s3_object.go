package ops

import (
	"context"
	"fmt"
	"io"
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

	logger.InfoContext(ctx, "backup.s3.put",
		slog.String("bucket", bucket),
		slog.String("key", key),
		slog.Int64("bytes", info.Size()),
	)
	return nil
}

// getObjectToFile downloads bucket/key to a local file at path, streaming the
// body to disk. Used by the restore drill to stage backup artifacts before
// loading them into a scratch engine.
func getObjectToFile(ctx context.Context, client *s3.Client, bucket, key, path string) error {
	logger := telemetry.L(ctx)
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		wrapped := fmt.Errorf("get object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer out.Body.Close()

	f, err := os.Create(path)
	if err != nil {
		wrapped := fmt.Errorf("create %s for download of %s/%s: %w", path, bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer f.Close()

	written, err := io.Copy(f, out.Body)
	if err != nil {
		wrapped := fmt.Errorf("write %s from %s/%s: %w", path, bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.s3.get",
		slog.String("bucket", bucket),
		slog.String("key", key),
		slog.Int64("bytes", written),
	)
	return nil
}

// listImmediatePrefixes returns the immediate child prefixes under prefix using
// the delimiter, for example the per-run folders under "yugabyte-snapshot/" or
// the backup names under "backups/". The returned values include the trailing
// delimiter, matching S3 CommonPrefixes.
func listImmediatePrefixes(ctx context.Context, client *s3.Client, bucket, prefix string) ([]string, error) {
	logger := telemetry.L(ctx)
	var out []string
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			wrapped := fmt.Errorf("list %s/%s: %w", bucket, prefix, err)
			logger.ErrorContext(ctx, "backup.s3.list_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		for _, cp := range page.CommonPrefixes {
			if cp.Prefix != nil {
				out = append(out, *cp.Prefix)
			}
		}
	}
	return out, nil
}
