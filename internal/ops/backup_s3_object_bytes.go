package ops

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/telemetry"
)

// smallObjectMaxBytes limits memory use for small records such as JSON
// status markers. Larger bounded records use getObjectBytesUpTo; large
// artifacts use getObjectToFile. The limit prevents hostile or corrupt
// objects from exhausting the staleness check's memory.
const smallObjectMaxBytes = 64 * 1024

// getObjectBytes downloads a small object into memory. Missing optional keys
// return a wrapped NotFound error without an error log. Oversized objects fail
// instead of exhausting the process that checks backup health.
func getObjectBytes(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	return getObjectBytesUpTo(ctx, client, bucket, key, smallObjectMaxBytes)
}

// getObjectBytesUpTo downloads an object with the caller's memory limit.
func getObjectBytesUpTo(
	ctx context.Context,
	client *s3.Client,
	bucket string,
	key string,
	maxBytes int,
) ([]byte, error) {
	logger := telemetry.L(ctx)
	out, err := client.GetObject(
		ctx,
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("get object %s/%s: %w", bucket, key, err)
		if !isObjectNotFound(err) {
			logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		}

		return nil, wrapped
	}
	defer out.Body.Close()
	// One extra byte distinguishes an exact-limit object from a larger one
	// without reading the entire oversized body.
	body, err := io.ReadAll(io.LimitReader(out.Body, int64(maxBytes)+1))
	if err != nil {
		wrapped := fmt.Errorf("read object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))

		return nil, wrapped
	}

	if len(body) > maxBytes {
		wrapped := fmt.Errorf(
			"object %s/%s is larger than the %d byte limit for in-memory reads",
			bucket,
			key,
			maxBytes,
		)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))

		return nil, wrapped
	}

	return body, nil
}
