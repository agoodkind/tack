// Staleness alarms send one email per mechanism when it first becomes stale.
// Recovery clears the recorded fault only after a fresh reading confirms it.
// Unreadable readings cannot clear a fault, even with a recent historical age.
// Local and shared state reconcile by generation to recognize other checkers'
// emails and recoveries. State records new faults only after mail acceptance;
// failed mail retries on the next run. Deputies defer new faults while the
// audit ledger confirms recent primary activity.

package ops

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// alarmBackupStalenessTransitions sends new fault emails and updates alarm
// state. Mail and state failures do not change the run's staleness verdict.
func alarmBackupStalenessTransitions(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	metrics []backupStalenessMetric,
) {
	logger := telemetry.L(ctx)
	memory := loadBackupAlarmMemory(
		ctx,
		cfg,
		func(key string) ([]byte, error) {
			return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
		},
	)
	state := memory.state
	var faults []backupStalenessMetric
	var held, cleared []string

	for _, metric := range metrics {
		_, alarmed := state.Alarmed[metric.Name]
		switch {
		case metric.stale() && !alarmed:
			faults = append(faults, metric)
		case metric.stale():
			held = append(held, metric.Name)
		case alarmed && metric.Unknown != backupStalenessAgeKnown:
			// A historical reading cannot establish current recovery.
			held = append(held, metric.Name)
		case alarmed:
			cleared = append(cleared, metric.Name)
			delete(state.Alarmed, metric.Name)
		}
	}

	if len(held) > 0 {
		logger.InfoContext(ctx, "backup.staleness.alarm_held", slog.Any("metrics", held))
	}

	if len(cleared) > 0 {
		logger.InfoContext(ctx, "backup.staleness.cleared", slog.Any("metrics", cleared))
	}

	changed := len(cleared) > 0
	if len(faults) > 0 && backupAlarmDeferredToPrimary(ctx, cfg, faults) {
		faults = nil
	}

	if len(faults) > 0 && mailBackupStalenessAlarm(ctx, cfg, faults) {
		acceptedAt := opsNow().UTC()

		for _, fault := range faults {
			state.Alarmed[fault.Name] = acceptedAt
		}

		changed = true
	}

	saveBackupAlarmMemory(
		ctx,
		cfg,
		func(key string, body []byte) error {
			return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
		},
		memory,
		state,
		changed,
	)
}
