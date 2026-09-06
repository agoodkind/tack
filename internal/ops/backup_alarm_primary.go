// backup_alarm_primary.go keeps one fault to one mail when the staleness check
// runs on more than one guest. Every guest keeps its own alarm memory, so
// without coordination each of them mails the same fault (TACK-481). The
// object store cannot carry the coordination: the fault that produced those
// duplicate mails was the store refusing writes, so shared state there fails
// exactly when the alarm is needed. Instead one checker is the primary and the
// others are deputies. A deputy asks the primary's health URL before mailing;
// while the primary answers, the deputy leaves the fault unmailed and
// unrecorded, so the fault mails from the deputy on a later run if the primary
// disappears while it is still stale.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmPrimaryProbeTimeout bounds the probe so a primary that hangs
// cannot hold the alarm.
const backupAlarmPrimaryProbeTimeout = 5 * time.Second

// backupAlarmPrimaryProbeFunc asks the primary whether it is alive. It is a
// package variable for the same reason backupAlarmSendFunc is, so a test can
// substitute it; tests prefer a real httptest server.
var backupAlarmPrimaryProbeFunc = probeBackupAlarmPrimary

// backupAlarmDeferredToPrimary reports whether a deputy should leave these
// faults to the primary. A checker with no primary URL is the primary and
// never defers. A deputy defers only when the primary answers 2xx; a dial
// error or timeout is logged by the probe, any other status is logged here,
// and in both cases the deputy mails as the primary would.
func backupAlarmDeferredToPrimary(ctx context.Context, cfg *config.Config, faults []backupStalenessMetric) bool {
	primary := cfg.BackupAlarmPrimaryURL
	if primary == "" {
		return false
	}
	logger := telemetry.L(ctx)
	status, err := backupAlarmPrimaryProbeFunc(ctx, primary)
	if err != nil {
		return false
	}
	if status < http.StatusOK || status > http.StatusIMUsed {
		logger.WarnContext(ctx, "backup.staleness.primary_unreachable",
			slog.String("primary", primary), slog.Int("status", status))
		return false
	}
	names := make([]string, 0, len(faults))
	for _, fault := range faults {
		names = append(names, fault.Name)
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_deferred",
		slog.Any("metrics", names), slog.String("primary", primary))
	return true
}

// probeBackupAlarmPrimary sends one GET to the primary's health URL and
// returns the response status. The body is not read: whether the primary is
// alive is the only question, and its answer is the status line. A primary
// that cannot be reached is logged here, the same way an unreachable master
// is, and returned as the error the caller mails on.
func probeBackupAlarmPrimary(ctx context.Context, url string) (int, error) {
	logger := telemetry.L(ctx)
	reqCtx, cancel := context.WithTimeout(ctx, backupAlarmPrimaryProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		wrapped := fmt.Errorf("build primary probe request for %s: %w", url, err)
		logger.WarnContext(ctx, "backup.staleness.primary_unreachable",
			slog.String("primary", url), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("get %s: %w", url, err)
		logger.WarnContext(ctx, "backup.staleness.primary_unreachable",
			slog.String("primary", url), slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
