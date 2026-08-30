package ops

import (
	"bytes"
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

// putObjectBytes uploads an in-memory body to bucket under key. Used for the
// backup-status success markers, which are a few hundred bytes; anything large
// enough to stage on disk goes through putObjectFromFile instead.
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

// getObjectBytes downloads bucket/key into memory. A missing key comes back as
// a NotFound-wrapped error without an error log, because callers probe for
// optional objects and treat absence as a state rather than a failure.
func getObjectBytes(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	logger := telemetry.L(ctx)
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		wrapped := fmt.Errorf("get object %s/%s: %w", bucket, key, err)
		if !isObjectNotFound(err) {
			logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		}
		return nil, wrapped
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		wrapped := fmt.Errorf("read object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return body, nil
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

// listImmediateObjects returns the keys of objects that sit directly under
// prefix, using the delimiter so keys nested in subfolders are excluded, and
// skipping a directory-placeholder object equal to prefix itself. The
// FoundationDB blobstore registers each backup as a zero-byte marker object at
// backups/<name> and keeps the backup's data in its own internal layout, so the
// restore drill discovers the backup name from these markers;
// listImmediatePrefixes would instead return the engine's internal data folder.
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
