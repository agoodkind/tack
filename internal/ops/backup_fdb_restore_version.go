// Restore-version lookup converts a requested time using object-store version
// records without a surviving FoundationDB cluster (TACK-468).

package ops

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// RunBackupFDBRestoreVersion prints the selected version and observation time
// to show the interval before the requested restore moment.
func RunBackupFDBRestoreVersion(
	ctx context.Context,
	cfg *config.Config,
	want time.Time,
	out io.Writer,
) error {
	logger := telemetry.L(ctx)
	if cfg.BackupS3Endpoint == "" || cfg.BackupS3AccessKey == "" || cfg.BackupS3SecretKey == "" {
		err := fmt.Errorf(
			"restore-version: TACK_BACKUP_S3_ENDPOINT, _ACCESS_KEY_ID, and _SECRET_ACCESS_KEY are required",
		)
		logger.ErrorContext(ctx, "backup.fdb_versions.failed", slog.String("err", err.Error()))

		return err
	}

	s3Client := newBackupS3Client(cfg)
	log, err := readFDBVersionLog(ctx, fdbVersionLogGetter(ctx, s3Client, cfg.BackupS3BucketMain))
	if err != nil {
		return err
	}

	if len(log.Points) == 0 {
		err := fdbVersionReadingsMissingError{Key: fdbVersionLogKey}
		logger.ErrorContext(ctx, "backup.fdb_versions.failed", slog.String("err", err.Error()))

		return err
	}

	point, found := fdbVersionAt(log, want.UTC())
	if !found {
		err := fmt.Errorf(
			"restore-version: the oldest recorded reading is %s, after %s",
			log.Points[0].At.Format(time.RFC3339),
			want.UTC().Format(time.RFC3339),
		)
		logger.ErrorContext(ctx, "backup.fdb_versions.failed", slog.String("err", err.Error()))

		return err
	}

	newest := log.Points[len(log.Points)-1]
	_, err = fmt.Fprintf(
		out,
		"restore to version %d\nthat version was restorable through %s, the last reading at or before %s\nnewest reading %s, %d readings recorded\n",
		point.Version,
		point.At.Format(time.RFC3339),
		want.UTC().Format(time.RFC3339),
		newest.At.Format(time.RFC3339),
		len(log.Points),
	)
	if err != nil {
		wrapped := fmt.Errorf("write the restore version: %w", err)
		logger.ErrorContext(ctx, "backup.fdb_versions.failed", slog.String("err", wrapped.Error()))

		return wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.fdb_versions.answered",
		slog.Int64("version", point.Version),
		slog.Time("at", point.At),
		slog.Time("want", want.UTC()),
	)

	return nil
}

type fdbVersionReadingsMissingError struct {
	Key string
}

func (failure fdbVersionReadingsMissingError) Error() string {
	return fmt.Sprintf(
		"restore-version: %s does not contain readings; time-to-version conversion requires a recorded reading",
		failure.Key,
	)
}
