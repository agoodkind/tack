// The transition policy suppresses repeated mail for a persistent stale
// mechanism after a successful send result. A fresh observation logs recovery
// and removes the alarm record; a later fault can then mail again. An
// unreadable observation cannot clear a record, even when an earlier reading
// still dates the mechanism as fresh.
//
// Local state and shared object memory reconcile by generation. Each checker
// honors the other's mail and recovery records. The policy records a new fault
// only after a successful send result and retries after an error. A QUIT error
// can follow server acceptance of DATA; a retry can repeat an accepted message.
// A deputy checks the audit ledger for a recent primary run and defers mailing
// and recording new faults while that evidence remains recent.

package ops

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// alarmBackupStalenessTransitions sends newly stale mechanisms and updates
// alarm memory. The caller preserves the staleness verdict regardless of mail
// or memory failures.
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
			// An earlier observation cannot establish current recovery. Keep the alarm
			// record until a fresh observation clears it.
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
