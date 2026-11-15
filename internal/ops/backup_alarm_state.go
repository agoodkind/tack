// Local alarm state records accepted fault email to suppress duplicates across
// repeated checks. Each guest caches state under its mounted backup root and
// reconciles shared state separately. Absent or unreadable local state permits
// an initial email unless shared evidence or primary deferral suppresses it.

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

// backupAlarmStateFile identifies the local fault acceptance record.
const backupAlarmStateFile = "staleness-alarm-state.json"

// backupAlarmState records metric acceptance times in UTC. Generation counts
// shared writes and identifies the shared generation last cached locally.
type backupAlarmState struct {
	Alarmed    map[string]time.Time `json:"alarmed"`
	Generation int                  `json:"generation"`
}

// backupAlarmStatePath returns the configured local state path.
func backupAlarmStatePath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupAlarmStateFile)
}

// loadBackupAlarmState returns empty initial state for an absent file. Read or
// decode failures log their cause and permit alarm delivery instead of trusting
// damaged acceptance records.
func loadBackupAlarmState(ctx context.Context, cfg *config.Config) backupAlarmState {
	empty := backupAlarmState{Alarmed: map[string]time.Time{}, Generation: 0}
	path := backupAlarmStatePath(cfg)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty
	}

	if err != nil {
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.alarm_state_unreadable",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return empty
	}

	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.alarm_state_unreadable",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return empty
	}

	if state.Alarmed == nil {
		state.Alarmed = map[string]time.Time{}
	}

	return state
}

// saveBackupAlarmState atomically replaces the prior record by rename to avoid
// partial state after interruption. Failed writes log the cause and permit
// repeated delivery rather than suppressing an unrecorded alarm.
func saveBackupAlarmState(ctx context.Context, cfg *config.Config, state backupAlarmState) {
	logger := telemetry.L(ctx)
	path := backupAlarmStatePath(cfg)
	body, err := json.Marshal(state)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	if err := os.MkdirAll(cfg.BackupRoot, 0o750); err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	partial, err := stageBackupAlarmState(ctx, path, body)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_write_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return
	}

	if err := os.Rename(partial, path); err != nil {
		// This invocation owns the temporary file that requires removal.
		_ = os.Remove(partial)
		renameErr := fmt.Errorf("rename %s into place: %w", partial, err)
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_write_failed",
			slog.String("path", path),
			slog.String("err", renameErr.Error()),
		)

		return
	}

	logger.InfoContext(
		ctx,
		"backup.staleness.alarm_state_written",
		slog.String("path", path),
		slog.Int("generation", state.Generation),
		slog.Int("alarmed_count", len(state.Alarmed)),
	)
}

// backupAlarmPartialSuffix sets temporary names for security tests.
var backupAlarmPartialSuffix = randomBackupAlarmPartialSuffix

// randomBackupAlarmPartialSuffix generates an unpredictable 64-bit suffix.
func randomBackupAlarmPartialSuffix(ctx context.Context) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		wrapped := fmt.Errorf("draw temporary name: %w", err)
		slog.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_partial_refused",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	return hex.EncodeToString(raw[:]), nil
}

// stageBackupAlarmState creates a unique adjacent temporary with exclusive,
// no-follow opening. The caller may rename only this returned path. Random
// suffixes isolate overlapping saves and prevent one writer from deleting or
// renaming another writer's bytes.
//
// Other temporary files may belong to active writers or interrupted runs.
// Timestamps cannot distinguish them safely, including the interval before a
// live writer's rename. This function leaves those small files for operators.
func stageBackupAlarmState(ctx context.Context, path string, body []byte) (string, error) {
	suffix, err := backupAlarmPartialSuffix(ctx)
	if err != nil {
		return "", err
	}

	partial := path + ".partial." + suffix
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		wrapped := fmt.Errorf("create %s: %w", partial, err)
		slog.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_partial_refused",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(partial)
		wrapped := fmt.Errorf("write %s: %w", partial, err)
		slog.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_partial_refused",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(partial)
		wrapped := fmt.Errorf("close %s: %w", partial, err)
		slog.ErrorContext(
			ctx,
			"backup.staleness.alarm_state_partial_refused",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	return partial, nil
}
