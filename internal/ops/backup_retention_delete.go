package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
)

const retentionDeleteBatchSize = 1000

type retentionDeleteError struct {
	cause     error
	requested int
	uncertain bool
}

func (err *retentionDeleteError) Error() string { return err.cause.Error() }
func (err *retentionDeleteError) Unwrap() error { return err.cause }

// deleteRetentionPrefix reads and removes bounded pages under one validated
// ledger run. A failed page stops deletion and returns confirmed delete counts.
func deleteRetentionPrefix(ctx context.Context, client *s3.Client, bucket, run string) (int, error) {
	if !ybRunIDPattern.MatchString(run) {
		return 0, fmt.Errorf("retention refuses invalid ledger run identifier %q", run)
	}
	prefix := ybSnapshotKeyPrefix(run)
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(retentionDeleteBatchSize),
	})
	deleted := 0
	requested := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "backup.retention.list_failed", slog.String("run_id", run), slog.String("err", err.Error()))
			return deleted, fmt.Errorf("list expired ledger run %s: %w", run, err)
		}
		identifiers := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			if object.Key == nil || !strings.HasPrefix(*object.Key, prefix) {
				return deleted, fmt.Errorf("retention refuses object outside ledger prefix %s", prefix)
			}
			identifiers = append(identifiers, s3types.ObjectIdentifier{Key: object.Key})
		}
		if len(identifiers) == 0 {
			continue
		}
		result, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: identifiers, Quiet: aws.Bool(false)},
		})
		requested += len(identifiers)
		if err != nil {
			var apiError smithy.APIError
			uncertain := !errors.As(err, &apiError) || apiError.ErrorCode() != "AccessDenied"
			return deleted, &retentionDeleteError{cause: fmt.Errorf("delete expired ledger run %s after %d confirmed objects: %w", run, deleted, err), requested: requested, uncertain: uncertain}
		}
		deleted += len(result.Deleted)
		if len(result.Errors) != 0 {
			failure := result.Errors[0]
			return deleted, &retentionDeleteError{cause: fmt.Errorf("delete expired ledger run %s partially completed: %d confirmed objects, %d refused objects (%s: %s)",
				run, deleted, len(result.Errors), aws.ToString(failure.Key), aws.ToString(failure.Code)), requested: requested, uncertain: false}
		}
		if len(result.Deleted) != len(identifiers) {
			return deleted, &retentionDeleteError{cause: fmt.Errorf("delete expired ledger run %s did not confirm every requested object", run), requested: requested, uncertain: true}
		}
	}
	return deleted, nil
}
