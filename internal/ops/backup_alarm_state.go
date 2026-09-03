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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
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
	partial, err := stageBackupAlarmState(ctx, path, body)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return
	}
	if err := os.Rename(partial, path); err != nil {
		// The temporary is this invocation's own, so removing it is safe.
		_ = os.Remove(partial)
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(ctx, "backup.staleness.alarm_state_write_failed",
			slog.String("path", path), slog.String("err", renameErr.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_state_written",
		slog.String("path", path), slog.Int("alarmed_count", len(state.Alarmed)))
}

// backupAlarmPartialSuffix names one invocation's temporary. It is a variable
// so a test can pin the name and plant an entry at it.
var backupAlarmPartialSuffix = randomBackupAlarmPartialSuffix

// randomBackupAlarmPartialSuffix draws 64 random bits, which no other
// invocation and no planter can predict.
func randomBackupAlarmPartialSuffix(ctx context.Context) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		wrapped := fmt.Errorf("draw temporary name: %w", err)
		slog.ErrorContext(ctx, "backup.staleness.alarm_state_partial_refused", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return hex.EncodeToString(raw[:]), nil
}

// stageBackupAlarmState writes body into a temporary of its own next to path
// and returns that temporary's name, which is the only file the caller may
// rename. The name carries a random suffix, so two invocations that overlap
// never share a temporary and neither can unlink or rename the other's bytes,
// and the open creates the name exclusively and refuses a symbolic link, the
// same way the audit verifier opens a bundle's rows.
//
// Nothing here removes another temporary. A file left by a crashed run keeps
// the fixed prefix with a suffix this invocation did not draw, and no safe
// test tells it apart from a live temporary of an overlapping invocation that
// started earlier: a modification time before this process started is exactly
// what that live temporary carries for the milliseconds between its write and
// its rename. Leftovers are small, harmless to the rename, and left to the
// operator.
func stageBackupAlarmState(ctx context.Context, path string, body []byte) (string, error) {
	suffix, err := backupAlarmPartialSuffix(ctx)
	if err != nil {
		return "", err
	}
	partial := path + ".partial." + suffix
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", partial, err)
		slog.ErrorContext(ctx, "backup.staleness.alarm_state_partial_refused", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(partial)
		wrapped := fmt.Errorf("write %s: %w", partial, err)
		slog.ErrorContext(ctx, "backup.staleness.alarm_state_partial_refused", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(partial)
		wrapped := fmt.Errorf("close %s: %w", partial, err)
		slog.ErrorContext(ctx, "backup.staleness.alarm_state_partial_refused", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return partial, nil
}
