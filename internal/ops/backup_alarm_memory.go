// Both checkers share alarm memory as one object under backup-status/. Each
// guest also stores a local JSON record with the same structure. The shared
// generation counts store writes. The local generation records the last shared
// generation used by that guest and advances locally during unreadable shared
// state changes. TACK-484 documented the need to accept
// another checker's recovery record despite a stale local fault.
//
// A newer shared generation replaces local state. An equal or older shared
// generation combines both sets of faults, including mail recorded only
// locally after a failed shared write. An unreadable store leaves local state
// in use and does not prevent mailing. The guest caches adopted shared faults
// locally to suppress duplicates during a later store outage.

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

// backupAlarmMemoryKey identifies the shared alarm object beside the
// per-mechanism success markers.
const backupAlarmMemoryKey = backupStatusPrefix + "alarm-state.json"

// backupAlarmMemory stores the run's selected state and the local and shared
// copies used to select it. readable is false after a failed store read; shared
// then contains an empty state at generation zero.
type backupAlarmMemory struct {
	state    backupAlarmState
	local    backupAlarmState
	shared   backupAlarmState
	readable bool
}

// loadBackupAlarmMemory reads both copies. It selects local state after an
// unreadable store, shared state when its generation exceeds the local
// generation, and their combined faults otherwise.
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

// fetchSharedBackupAlarmMemory reads shared state through get. An absent
// object returns a readable empty state at generation zero for a first run.
// Fetch and decode failures log a warning and return unreadable state; the
// caller then uses the local file.
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

// saveBackupAlarmMemory saves changed state or faults that the shared copy
// lacks. A readable store receives the next shared generation. A successful
// write records that generation locally; a failed write records the old shared
// generation locally for another combined write on a later run.
//
// With an unreadable store, a change advances only the local generation. A
// later readable run combines local state with an older shared copy instead of
// discarding the local change. Even without a new change, a readable shared
// copy updates a different local state or generation.
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

// putSharedBackupAlarmMemory encodes state and writes it through put. It
// reports whether the write succeeded and logs a failure. A later run can
// combine the local record with shared state and retry the write.
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

// unionBackupAlarmState includes each mechanism recorded in either copy and
// uses the earlier mail acceptance time for duplicates. Saving determines the
// generation.
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

// copyBackupAlarmState clones the entries for editing without changing the
// loaded state.
func copyBackupAlarmState(state backupAlarmState) backupAlarmState {
	return backupAlarmState{Alarmed: maps.Clone(state.Alarmed), Generation: state.Generation}
}

// sameBackupAlarms reports whether both copies record the same mechanisms
// and acceptance instants, regardless of generation.
func sameBackupAlarms(a, b backupAlarmState) bool {
	return maps.EqualFunc(a.Alarmed, b.Alarmed, time.Time.Equal)
}
