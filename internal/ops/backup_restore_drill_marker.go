// backup_restore_drill_marker.go records a passing restore drill in the object
// store. The marker put is retried on its own, because a put that fails after
// every leg has passed is the only step worth repeating: re-running the drill
// would repeat two restores to reach the same put.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// restoreDrillMarkerAttempts is how many times the rehearsal marker put is
// tried before the drill reports the marker missing.
const restoreDrillMarkerAttempts = 5

// restoreDrillMarkerFirstWait is the pause before the second put attempt. Each
// later pause doubles it, so the five attempts span about two minutes, long
// enough to ride out a brief object-store fault and short enough that a real
// outage still fails the run promptly.
const restoreDrillMarkerFirstWait = 8 * time.Second

// recordRestoreDrillRehearsal records a passing drill so the staleness check
// can tell how long ago recovery was last rehearsed. The marker is part of the
// drill's success, not a side effect: a drill nobody can date is
// indistinguishable from a drill that never ran, which is the failure the
// staleness alert exists to catch. put is bound to the object store by the
// caller, so what the drill records stays checkable against what the staleness
// check reads.
//
// A failed put is retried with a bounded backoff, and the marker keeps the
// time the drill passed rather than the time an attempt landed. When every
// attempt fails, or the context ends between attempts, the returned error
// names the marker as the failed step so the operator does not read it as a
// failed restore.
func recordRestoreDrillRehearsal(
	ctx context.Context,
	put func(key string, body []byte) error,
	runID string,
	legs []string,
) error {
	passedAt := opsNow().UTC()
	detail := "restore drill " + runID + " passed: " + strings.Join(legs, ", ")
	wait := restoreDrillMarkerFirstWait
	var putErr error
	for attempt := 1; attempt <= restoreDrillMarkerAttempts; attempt++ {
		putErr = writeBackupStatusMarker(ctx, put, backupStalenessRehearsalName, passedAt, detail)
		if putErr == nil {
			return nil
		}
		slog.ErrorContext(ctx, "backup.restore_drill.marker_put_failed",
			slog.Int("attempt", attempt),
			slog.Int("attempts", restoreDrillMarkerAttempts),
			slog.String("err", putErr.Error()))
		if attempt == restoreDrillMarkerAttempts {
			break
		}
		if !opsWait(ctx, wait) {
			slog.ErrorContext(ctx, "backup.restore_drill.marker_retry_canceled",
				slog.Int("attempt", attempt),
				slog.String("err", putErr.Error()))
			break
		}
		wait *= 2
	}
	wrapped := fmt.Errorf("restore-drill: every leg passed but the rehearsal marker did not land: %w", putErr)
	slog.ErrorContext(ctx, "backup.restore_drill.failed", slog.String("err", wrapped.Error()))
	return wrapped
}
