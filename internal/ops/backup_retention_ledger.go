package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
)

func planRetentionLedger(ctx context.Context, cfg *config.Config, client *s3.Client, cutoff time.Time) ([]string, error) {
	runs, err := listYBSnapshotRunIDs(ctx, client, cfg.BackupS3BucketMain)
	if err != nil {
		return nil, err
	}
	var expired, retained []string
	for _, run := range runs {
		stamp, err := time.Parse(ybSnapshotRunIDLayout, run)
		if err != nil || !ybRunIDPattern.MatchString(run) {
			return nil, fmt.Errorf("retention refuses invalid ledger run identifier %q", run)
		}
		if stamp.Before(cutoff) {
			// A present manifest must validate even when its export is incomplete.
			if _, err := fetchYBSnapshotManifest(ctx, client, cfg.BackupS3BucketMain, run); err != nil && !isObjectNotFound(err) {
				return nil, err
			}
			expired = append(expired, run)
		} else {
			retained = append(retained, run)
		}
	}
	if len(expired) == 0 {
		return nil, nil
	}
	_, complete, err := newestCompleteYBSnapshotRun(retained,
		func(run string) (ybSnapshotManifest, error) {
			return fetchYBSnapshotManifest(ctx, client, cfg.BackupS3BucketMain, run)
		},
		func(key string) (bool, error) {
			return objectExists(ctx, client, cfg.BackupS3BucketMain, key)
		},
		func(run, reason string) {
			slog.InfoContext(ctx, "backup.retention.retained_run_incomplete", slog.String("run_id", run), slog.String("reason", reason))
		})
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, fmt.Errorf("retention refuses ledger deletion: a complete export at or after %s is required", cutoff.Format(time.RFC3339))
	}
	return expired, nil
}
