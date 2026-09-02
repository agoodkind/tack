// backup_alarm_state.go remembers which backup mechanisms the staleness alarm
// has already mailed about, so a fault mails once when it begins and not on
// every run while it lasts. The record is one small JSON file under the backup
// root, which the check's container mounts from the guest, so each observing
// guest keeps its own memory and a fault produces one mail per guest. A missing
// or unreadable file means nothing has been alarmed: a fresh guest mails once
// and then stops.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmStateFile is the file under the backup root that records the
// alarmed mechanisms.
const backupAlarmStateFile = "staleness-alarm-state.json"

// backupAlarmState is the alarm's memory: each mechanism whose fault has been
// mailed, keyed by metric name, with the UTC instant the mail was accepted.
type backupAlarmState struct {
	Alarmed map[string]time.Time `json:"alarmed"`
}

// backupAlarmStatePath is where the state lives for this configuration.
func backupAlarmStatePath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupAlarmStateFile)
}

// loadBackupAlarmState reads the alarm's memory. An absent file is the ordinary
// first run and returns an empty state silently; a file that exists but cannot
// be read or decoded is logged and also treated as empty, so the alarm still
// mails rather than staying silent behind a damaged record.
func loadBackupAlarmState(ctx context.Context, cfg *config.Config) backupAlarmState {
	empty := backupAlarmState{Alarmed: map[string]time.Time{}}
	path := backupAlarmStatePath(cfg)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty
	}
	if err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_state_unreadable",
			slog.String("path", path), slog.String("err", err.Error()))
		return empty
	}
	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_state_unreadable",
			slog.String("path", path), slog.String("err", err.Error()))
		return empty
	}
	if state.Alarmed == nil {
		state.Alarmed = map[string]time.Time{}
	}
	return state
}

// saveBackupAlarmState writes the alarm's memory in place of whatever was
// there, through a rename so a run interrupted mid-write leaves the previous
// record rather than a truncated one. A write that fails is logged and the
// alarm continues: the next run will not remember the mail it just sent and
// will mail again, which is the failure mode that loses least.
func saveBackupAlarmState(ctx context.Context, cfg *config.Config, state backupAlarmState) {
	logger := telemetry.L(ctx)
	path := backupAlarmStatePath(cfg)
	body, err := json.Marshal(state)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	if err := os.MkdirAll(cfg.BackupRoot, 0o750); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	partial := path + ".partial"
	if err := os.WriteFile(partial, body, 0o600); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	if err := os.Rename(partial, path); err != nil {
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", renameErr.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_state_written",
		slog.String("path", path), slog.Int("alarmed_count", len(state.Alarmed)))
}
