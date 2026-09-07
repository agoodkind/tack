// backup_alarm_policy.go decides when the staleness alarm mails: once per
// mechanism, on the run that first finds it stale, and never again while it
// stays stale. A mechanism that comes back is logged and forgotten, so its next
// fault mails again. The memory is the state file in backup_alarm_state.go
// merged with the copy both checkers share in the object store
// (backup_alarm_memory.go), so a fault the other checker mailed is held here
// too. A mechanism is recorded only after the transport accepted its mail, so
// a mail that did not go out is retried on the next run, and every change is
// written to both copies. A deputy checker first asks the ledger whether the
// primary has run recently (backup_alarm_primary_ledger.go) and, while it has,
// neither mails nor records the new faults.

package ops

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// alarmBackupStalenessTransitions mails the mechanisms that became stale on
// this run and updates the alarm's memory. It returns nothing: the stale
// verdict is the run's result whatever the mail or the memory did.
func alarmBackupStalenessTransitions(
	ctx context.Context,
	cfg *config.Config,
	s3Client *s3.Client,
	metrics []backupStalenessMetric,
) {
	logger := telemetry.L(ctx)
	shared := loadSharedBackupAlarmMemory(ctx, func(key string) ([]byte, error) {
		return getObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key)
	})
	state := mergeBackupAlarmState(loadBackupAlarmState(ctx, cfg), shared)
	var faults []backupStalenessMetric
	var held, cleared []string
	for _, metric := range metrics {
		_, alarmed := state.Alarmed[metric.Name]
		switch {
		case metric.stale() && !alarmed:
			faults = append(faults, metric)
		case metric.stale():
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
	if changed {
		saveBackupAlarmState(ctx, cfg, state)
		saveSharedBackupAlarmMemory(ctx, func(key string, body []byte) error {
			return putObjectBytes(ctx, s3Client, cfg.BackupS3BucketMain, key, body)
		}, state)
	}
}
