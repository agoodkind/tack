// backup_alarm_memory.go is the alarm memory both checkers share: one object
// under backup-status/ in the object store, the same shape as the state file
// each guest keeps under its backup root (backup_alarm_state.go). A run reads
// the shared copy, merges it with its own file so a fault either checker has
// mailed counts as mailed, and writes the merged state back to both after a
// mail is accepted or a fault clears (TACK-484). The store is a supplement to
// the local file and never a gate on it: a copy that cannot be read is logged
// and the run continues on the file alone, so an unreachable store cannot keep
// a mail from going out, and a copy that cannot be written is logged and left
// for the next run, which writes the merged state again.

package ops

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmMemoryKey is the object key of the shared alarm memory, beside
// the per-mechanism success markers.
const backupAlarmMemoryKey = backupStatusPrefix + "alarm-state.json"

// loadSharedBackupAlarmMemory reads the shared memory through get, which the
// caller binds to the object store. An absent object is the ordinary first run
// and returns an empty state silently; an object that cannot be fetched or
// decoded is logged and also treated as empty, so the alarm still mails from
// the local file rather than staying silent behind a store it cannot read.
func loadSharedBackupAlarmMemory(ctx context.Context, get func(key string) ([]byte, error)) backupAlarmState {
	empty := backupAlarmState{Alarmed: map[string]time.Time{}}
	body, err := get(backupAlarmMemoryKey)
	if err != nil {
		if isObjectNotFound(err) {
			return empty
		}
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return empty
	}
	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return empty
	}
	if state.Alarmed == nil {
		state.Alarmed = map[string]time.Time{}
	}
	return state
}

// saveSharedBackupAlarmMemory writes state to the shared memory through put,
// which the caller binds to the object store. A write that fails is logged and
// the local file keeps the record: the other checker may mail the same fault
// once more, and the next run here writes the merged state again.
func saveSharedBackupAlarmMemory(ctx context.Context, put func(key string, body []byte) error, state backupAlarmState) {
	logger := telemetry.L(ctx)
	body, err := json.Marshal(state)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return
	}
	if err := put(backupAlarmMemoryKey, body); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_memory_written",
		slog.String("key", backupAlarmMemoryKey), slog.Int("alarmed_count", len(state.Alarmed)))
}

// mergeBackupAlarmState unions two memories: a mechanism alarmed in either is
// alarmed in the result, dated by the earlier of the two accept times, which
// is when the fault first mailed.
func mergeBackupAlarmState(local, shared backupAlarmState) backupAlarmState {
	merged := backupAlarmState{Alarmed: make(map[string]time.Time, len(local.Alarmed)+len(shared.Alarmed))}
	maps.Copy(merged.Alarmed, local.Alarmed)
	for name, acceptedAt := range shared.Alarmed {
		if existing, ok := merged.Alarmed[name]; ok && !acceptedAt.Before(existing) {
			continue
		}
		merged.Alarmed[name] = acceptedAt
	}
	return merged
}
