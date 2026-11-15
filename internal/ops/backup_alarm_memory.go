// Backup checkers reconcile shared and local alarm state by generation (TACK-484).
// Local state supports alarm mail during object-store failures and caches shared
// alarms to avoid duplicate mail. Failed shared writes require a later retry.

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

// backupAlarmMemoryKey identifies the shared object for alarm state.
const backupAlarmMemoryKey = backupStatusPrefix + "alarm-state.json"

// backupAlarmMemory stores the selected state and both original copies.
// readable reports whether the checker could read the shared copy.
type backupAlarmMemory struct {
	state    backupAlarmState
	local    backupAlarmState
	shared   backupAlarmState
	readable bool
}

// loadBackupAlarmMemory selects the local copy when the shared read fails,
// the shared copy when its generation is newer, and their union otherwise.
func loadBackupAlarmMemory(
	ctx context.Context,
	cfg *config.Config,
	get func(key string) ([]byte, error),
) backupAlarmMemory {
	local := loadBackupAlarmState(ctx, cfg)
	shared, readable := fetchSharedBackupAlarmMemory(ctx, get)
	memory := backupAlarmMemory{state: local, local: local, shared: shared, readable: readable}
	switch {
	case !readable:
		memory.state = copyBackupAlarmState(local)
	case shared.Generation > local.Generation:
		telemetry.L(ctx).InfoContext(
			ctx,
			"backup.staleness.alarm_memory_adopted",
			slog.Int("generation", shared.Generation),
			slog.Int("local_generation", local.Generation),
		)
		memory.state = copyBackupAlarmState(shared)
	default:
		memory.state = unionBackupAlarmState(local, shared)
	}

	return memory
}

// fetchSharedBackupAlarmMemory reads the shared object. A missing object
// represents empty state at generation zero. A fetch or decode failure logs
// the error and requires the caller to use local state.
func fetchSharedBackupAlarmMemory(
	ctx context.Context,
	get func(key string) ([]byte, error),
) (backupAlarmState, bool) {
	empty := backupAlarmState{Alarmed: map[string]time.Time{}, Generation: 0}
	body, err := get(backupAlarmMemoryKey)
	if err != nil {
		if isObjectNotFound(err) {
			return empty, true
		}

		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey),
			slog.String("err", err.Error()),
		)

		return empty, false
	}

	var state backupAlarmState
	if err := json.Unmarshal(body, &state); err != nil {
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.staleness.alarm_memory_unreadable",
			slog.String("key", backupAlarmMemoryKey),
			slog.String("err", err.Error()),
		)

		return empty, false
	}

	if state.Alarmed == nil {
		state.Alarmed = map[string]time.Time{}
	}

	return state, true
}

// saveBackupAlarmMemory writes changed alarms or missing shared alarms at the
// next shared generation. A failed shared write preserves the old generation
// locally for a later retry. If the shared read failed, only a changed state
// advances the local generation. An unchanged run still caches shared state
// when the local alarms or generation differ.
func saveBackupAlarmMemory(
	ctx context.Context,
	cfg *config.Config,
	put func(key string, body []byte) error,
	memory backupAlarmMemory,
	state backupAlarmState,
	changed bool,
) {
	storeBehind := memory.readable && !sameBackupAlarms(state, memory.shared)
	fileBehind := !sameBackupAlarms(state, memory.local) ||
		memory.local.Generation != memory.shared.Generation
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

// putSharedBackupAlarmMemory reports whether the shared write succeeded.
// The next run retries failed writes after merging local state.
func putSharedBackupAlarmMemory(
	ctx context.Context,
	put func(key string, body []byte) error,
	state backupAlarmState,
) bool {
	logger := telemetry.L(ctx)
	body, err := json.Marshal(state)
	if err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey),
			slog.String("err", err.Error()),
		)

		return false
	}

	if err := put(backupAlarmMemoryKey, body); err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_memory_write_failed",
			slog.String("key", backupAlarmMemoryKey),
			slog.String("err", err.Error()),
		)

		return false
	}

	logger.InfoContext(
		ctx,
		"backup.staleness.alarm_memory_written",
		slog.String("key", backupAlarmMemoryKey),
		slog.Int("generation", state.Generation),
		slog.Int("alarmed_count", len(state.Alarmed)),
	)

	return true
}

// unionBackupAlarmState includes alarms from both copies at the earlier
// acceptance time. The save operation sets the generation.
func unionBackupAlarmState(local, shared backupAlarmState) backupAlarmState {
	combinedState := copyBackupAlarmState(local)

	for name, acceptedAt := range shared.Alarmed {
		if existing, ok := combinedState.Alarmed[name]; ok && !acceptedAt.Before(existing) {
			continue
		}

		combinedState.Alarmed[name] = acceptedAt
	}

	return combinedState
}

func copyBackupAlarmState(state backupAlarmState) backupAlarmState {
	return backupAlarmState{Alarmed: maps.Clone(state.Alarmed), Generation: state.Generation}
}

// sameBackupAlarms compares mechanisms and times, ignoring generations.
func sameBackupAlarms(a, b backupAlarmState) bool {
	return maps.EqualFunc(a.Alarmed, b.Alarmed, time.Time.Equal)
}
