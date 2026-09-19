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

// smallObjectMaxBytes bounds what getObjectBytes will hold in memory. Its
// callers read status markers, which are a few hundred bytes of JSON, so this
// is generous by four orders of magnitude and still small enough that a
// corrupted or hostile object cannot exhaust the process. Use
// [getObjectToFile] for artifacts that are legitimately large.
const smallObjectMaxBytes = 64 * 1024

// getObjectBytes downloads a small bucket/key into memory. A missing key comes
// back as a NotFound-wrapped error without an error log, because callers probe
// for optional objects and treat absence as a state rather than a failure. A
// body past smallObjectMaxBytes is refused rather than read: the staleness
// check is the mechanism that reports silence, so it must not be the thing an
// oversized object takes down.
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
	// Read one byte past the limit so an object sitting exactly at it still
	// reads cleanly while anything larger is detectable without buffering it.
	body, err := io.ReadAll(io.LimitReader(out.Body, smallObjectMaxBytes+1))
	if err != nil {
		wrapped := fmt.Errorf("read object %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if len(body) > smallObjectMaxBytes {
		wrapped := fmt.Errorf("object %s/%s is larger than the %d byte limit for in-memory reads",
			bucket, key, smallObjectMaxBytes)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return body, nil
}
