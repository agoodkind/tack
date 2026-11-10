// The alarm stores previously mailed mechanisms in a small JSON file under
// the backup root. The check container mounts that root from the guest, giving
// each observing guest a local copy. Shared object memory uses the same
// structure, and deputies can defer to recent primary ledger evidence during
// an object-store outage.
//
// A missing or unreadable local file starts with an empty local record. A
// fresh guest can mail once unless shared memory already records the fault or
// recent primary evidence defers it. Successful acceptance records suppress
// later messages while the fault persists.

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

// backupAlarmStateFile identifies the file under the backup root that records
// alarmed mechanisms.
const backupAlarmStateFile = "staleness-alarm-state.json"

// backupAlarmState indexes mailed mechanisms by metric name and records each
// mail's UTC acceptance time. Generation counts shared writes. A local file
// records its last shared generation and advances locally when shared state is
// unreadable and the alarm state changes.
type backupAlarmState struct {
	Alarmed    map[string]time.Time `json:"alarmed"`
	Generation int                  `json:"generation"`
}

// backupAlarmStatePath returns the configured local alarm state path.
func backupAlarmStatePath(cfg *config.Config) string {
	return filepath.Join(cfg.BackupRoot, backupAlarmStateFile)
}

// loadBackupAlarmState reads local alarm memory. An absent file returns an
// empty state without logging. Read and decode failures log a warning and also
// return empty state, permitting an alarm despite a damaged local record.
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

// saveBackupAlarmState stages the new record and renames it over the prior
// file. An interruption during staging does not truncate the prior record.
// Write failures log an error without stopping the alarm. A later run can mail
// again if neither shared memory nor the previous local record records
// acceptance. Duplicate mail is preferable to suppressing an unrecorded fault.
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
		// This invocation created the temporary file and can remove it after its
		// rename fails.
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

// backupAlarmPartialSuffix supplies the temporary file suffix for one
// invocation. Tests replace it to pre-create an entry at the expected path.
var backupAlarmPartialSuffix = randomBackupAlarmPartialSuffix

// randomBackupAlarmPartialSuffix generates 64 random bits for a temporary
// file suffix. Cryptographic randomness makes advance prediction impractical.
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

// stageBackupAlarmState writes body to its own temporary file beside path
// and returns the only path that the caller may rename. A random suffix
// separates concurrent attempts. Exclusive creation refuses an existing entry,
// and O_NOFOLLOW refuses a symbolic link, matching the audit bundle row opener.
// An accepted temporary belongs only to this invocation.
//
// This helper does not remove other temporary files. A crashed run and an
// earlier overlapping run can leave indistinguishable entries with the fixed
// prefix and a different suffix. Modification time cannot safely distinguish
// them: a live run can write before this process starts and rename afterward.
// These small leftover files do not prevent replacement of the state file;
// the operator manages their removal.
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
