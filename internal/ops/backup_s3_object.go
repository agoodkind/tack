package ops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/telemetry"
)

// putObjectFromFile uploads a local file to bucket under key. It passes the
// open file to the S3 client instead of reading the whole file into memory.
// The seekable [os.File] supports retries, and the file size supplies
// Content-Length. Backup operations use the client from newBackupS3Client to
// upload artifacts to the SeaweedFS object store.
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

// putObjectBytes uploads an in-memory body to bucket under key. Backup-status
// success markers use this helper for their few hundred bytes. Larger
// artifacts that require disk staging use putObjectFromFile.
func putObjectBytes(ctx context.Context, client *s3.Client, bucket, key string, body []byte) error {
	logger := telemetry.L(ctx)
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
	})
	if err != nil {
		wrapped := fmt.Errorf("put object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.put_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.s3.put",
		slog.String("bucket", bucket),
		slog.String("key", key),
		slog.Int("bytes", len(body)),
	)
	return nil
}

// listImmediatePrefixes returns immediate child prefixes using the /
// delimiter. Examples include per-run folders under "yugabyte-snapshot/" and
// backup names under "backups/". Each returned value includes the trailing
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

// listImmediateObjects returns object keys directly under prefix. The /
// delimiter excludes nested keys. The helper skips a directory placeholder
// with a key equal to prefix.
//
// The FoundationDB blobstore registers each backup with a zero-byte marker
// at backups/<name>. It stores backup data in a separate internal layout.
// The restore drill discovers backup names from these markers.
// listImmediatePrefixes would return the engine's internal data folder.
func listImmediateObjects(ctx context.Context, client *s3.Client, bucket, prefix string) ([]string, error) {
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
			wrapped := fmt.Errorf("list objects %s/%s: %w", bucket, prefix, err)
			logger.ErrorContext(ctx, "backup.s3.list_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		for _, obj := range page.Contents {
			if obj.Key != nil && *obj.Key != prefix {
				out = append(out, *obj.Key)
			}
		}
	}
	return out, nil
}
