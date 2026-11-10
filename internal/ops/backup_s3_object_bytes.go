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

// smallObjectMaxBytes limits the memory that getObjectBytes uses for a body.
// Status markers contain a few hundred bytes of JSON. The limit leaves room
// for these markers while bounding memory use for corrupt or hostile bodies.
// Records with a larger known bound use getObjectBytesUpTo with that bound.
// Large artifacts use
// [goodkind.io/tack/internal/ops/objectstage.GetObjectToFile].
const smallObjectMaxBytes = 64 * 1024

// getObjectBytes downloads a small object at bucket/key into memory. It wraps
// a missing-key error without logging an error. Callers probe optional
// objects and treat absence as a state. The helper rejects bodies larger
// than smallObjectMaxBytes after a bounded read. This limit protects the
// staleness check from oversized objects while it checks for missing success
// records.
func getObjectBytes(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	return getObjectBytesUpTo(ctx, client, bucket, key, smallObjectMaxBytes)
}

// getObjectBytesUpTo reads an object like getObjectBytes, with maxBytes as
// the in-memory body limit.
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
	// Read at most one byte beyond the limit. A body exactly at the limit
	// remains valid, and the extra byte identifies an oversized body without
	// reading the rest.
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
