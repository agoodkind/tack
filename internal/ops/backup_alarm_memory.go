// backup_alarm_memory.go is the alarm memory both checkers share: one object
// under backup-status/ in the object store, the same shape as the state file
// each guest keeps under its backup root (backup_alarm_state.go), carrying a
// generation that counts the store's writes. The file carries the store
// generation it last synced with, and the pair decides which copy a run
// believes (TACK-484). A store copy newer than the file is authoritative, so
// a clear the other checker recorded reaches this guest even while its file
// still holds the fault. A store copy no newer than the file is unioned with
// it, the state after this guest's own store write failed or after nothing
// changed, so a mail recorded only in the file is written into the store on
// the next run. A store that cannot be read leaves the file alone in charge,
// so an unreachable store never keeps a mail from going out, and every claim
// a run takes from the store is cached in the file, so a later unreadable
// store does not make this guest mail a held fault again.

package ops

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmMemoryKey is the object key of the shared alarm memory, beside
// the per-mechanism success markers.
const backupAlarmMemoryKey = backupStatusPrefix + "alarm-state.json"

// backupAlarmMemory is what one run loaded: the state it decides against and
// the two copies it was built from. readable is false when the store could
// not be read, in which case shared is empty at generation zero.
type backupAlarmMemory struct {
	state    backupAlarmState
	local    backupAlarmState
	shared   backupAlarmState
	readable bool
}

// loadBackupAlarmMemory reads both copies and picks the state the run decides
// against: the file alone when the store cannot be read, the store alone when
// its generation is past the file's, and the union of the two otherwise.
func loadBackupAlarmMemory(ctx context.Context, cfg *config.Config, get func(key string) ([]byte, error)) backupAlarmMemory {
	local := loadBackupAlarmState(ctx, cfg)
	shared, readable := fetchSharedBackupAlarmMemory(ctx, get)
	memory := backupAlarmMemory{state: local, local: local, shared: shared, readable: readable}
	switch {
	case !readable:
		memory.state = copyBackupAlarmState(local)
	case shared.Generation > local.Generation:
		telemetry.L(ctx).InfoContext(ctx, "backup.staleness.alarm_memory_adopted",
			slog.Int("generation", shared.Generation), slog.Int("local_generation", local.Generation))
		memory.state = copyBackupAlarmState(shared)
	default:
		memory.state = unionBackupAlarmState(local, shared)
	}
	return memory
}

// fetchSharedBackupAlarmMemory reads the store's copy through get. An absent
// object is the ordinary first run: readable, empty, generation zero. An
// object that cannot be fetched or decoded is logged and reported unreadable,
// so the caller decides from the file alone.
func fetchSharedBackupAlarmMemory(ctx context.Context, get func(key string) ([]byte, error)) (backupAlarmState, bool) {
	empty := backupAlarmState{Alarmed: map[string]time.Time{}, Generation: 0}
	body, err := get(backupAlarmMemoryKey)
	if err != nil {
		if isObjectNotFound(err) {
			return empty, true
		}
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return empty, false
	}
	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		telemetry.L(ctx).WarnContext(ctx, "backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return empty, false
	}
	if state.Alarmed == nil {
		state.Alarmed = map[string]time.Time{}
	}
	return state, true
}

// saveBackupAlarmMemory writes what the run decided back to the copies that
// need it. The store is written when the run changed something or when the
// union holds a claim the store lacks, at the generation after the store's;
// the file then takes that generation, or keeps the store's old one when the
// put failed so the next run unions and writes the store again. With the
// store unreadable a change advances the file's generation alone, so the next
// readable run unions rather than adopting an older store. A run that changed
// nothing still caches a state that differs from the file, at the store's
// generation.
func saveBackupAlarmMemory(
	ctx context.Context,
	cfg *config.Config,
	put func(key string, body []byte) error,
	memory backupAlarmMemory,
	state backupAlarmState,
	changed bool,
) {
	storeBehind := memory.readable && !sameBackupAlarms(state, memory.shared)
	fileBehind := !sameBackupAlarms(state, memory.local) || memory.local.Generation != memory.shared.Generation
	switch {
	case memory.readable && (changed || storeBehind):
		state.Generation = memory.shared.Generation + 1
		if !putSharedBackupAlarmMemory(ctx, put, state) {
			state.Generation = memory.shared.Generation
		}
		saveBackupAlarmState(ctx, cfg, state)
	case changed:
		state.Generation = memory.local.Generation + 1
		saveBackupAlarmState(ctx, cfg, state)
	case memory.readable && fileBehind:
		state.Generation = memory.shared.Generation
		saveBackupAlarmState(ctx, cfg, state)
	}
}

// putSharedBackupAlarmMemory writes state to the store through put and
// reports whether it landed. A write that fails is logged and left to the
// next run, which unions the file back in and writes the store again.
func putSharedBackupAlarmMemory(ctx context.Context, put func(key string, body []byte) error, state backupAlarmState) bool {
	logger := telemetry.L(ctx)
	body, err := json.Marshal(state)
	if err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return false
	}
	if err := put(backupAlarmMemoryKey, body); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey), slog.String("err", err.Error()))
		return false
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_memory_written",
		slog.String("key", backupAlarmMemoryKey), slog.Int("generation", state.Generation),
		slog.Int("alarmed_count", len(state.Alarmed)))
	return true
}

// unionBackupAlarmState joins two copies: a mechanism alarmed in either is
// alarmed in the result, dated by the earlier accept time, which is when the
// fault first mailed. The generation is settled at save time.
func unionBackupAlarmState(local, shared backupAlarmState) backupAlarmState {
	merged := copyBackupAlarmState(local)
	for name, acceptedAt := range shared.Alarmed {
		if existing, ok := merged.Alarmed[name]; ok && !acceptedAt.Before(existing) {
			continue
		}
		merged.Alarmed[name] = acceptedAt
	}
	return merged
}

// copyBackupAlarmState duplicates a copy's entries so the run can edit its
// state without touching what it loaded.
func copyBackupAlarmState(state backupAlarmState) backupAlarmState {
	return backupAlarmState{Alarmed: maps.Clone(state.Alarmed), Generation: state.Generation}
}

// sameBackupAlarms reports whether two copies alarm the same mechanisms at
// the same instants, whatever their generations.
func sameBackupAlarms(a, b backupAlarmState) bool {
	return maps.EqualFunc(a.Alarmed, b.Alarmed, time.Time.Equal)
}
