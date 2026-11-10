// The restore drill records its success marker in the object store. It retries
// a failed marker write separately after both restore checks pass. Restarting
// the entire drill would repeat both restores before attempting that write.

package ops

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// restoreDrillMarkerAttempts limits marker-write attempts before the drill
// reports that it could not record the marker.
const restoreDrillMarkerAttempts = 5

// restoreDrillMarkerFirstWait sets the pause before the second write attempt.
// Each later pause doubles. The five attempts include about two minutes of
// pauses to tolerate a brief object-store failure while bounding the wait
// before the drill reports an outage.
const restoreDrillMarkerFirstWait = 8 * time.Second

// restoreDrillMarkerExitStatus distinguishes a marker failure after every
// restore check passes. Its value differs from 1. The unit that runs the drill
// can use this status to avoid restarting both restores for a failed marker
// write.
const restoreDrillMarkerExitStatus = 3

// RestoreDrillMarkerError reports that every restore check passed but all
// marker-write attempts failed. Err stores the last write error.
type RestoreDrillMarkerError struct {
	Err error
}

// Error identifies the marker write as the failed step. It distinguishes this
// failure from a failed restore.
//
// The message includes the last object-store write error.
//
//   - The message starts with "restore-drill:".
func (e *RestoreDrillMarkerError) Error() string {
	return "restore-drill: every leg passed but the rehearsal marker did not land: " + e.Err.Error()
}

// Unwrap returns the last marker-write error.
//
// The method returns Err without replacing the original error.
//
//   - [errors.Is] and [errors.As] can inspect that underlying failure.
func (e *RestoreDrillMarkerError) Unwrap() error { return e.Err }

// OperatorExitStatus returns the CLI status for a marker-only failure.
//
// The method uses a distinct name from ExitCode. Other error types, including
// child-process errors, use ExitCode; the CLI must not confuse their statuses
// with this operator status.
//
//   - This failure uses status 3.
func (e *RestoreDrillMarkerError) OperatorExitStatus() int { return restoreDrillMarkerExitStatus }

// recordRestoreDrillRehearsal writes the passing drill's marker for the
// staleness check. A dated marker forms part of success: without it, the check
// cannot distinguish this rehearsal from a drill that did not run. The caller
// connects put to the object store that the staleness check reads.
//
// Failed writes use a bounded doubling delay. Every attempt records the time
// the drill passed, rather than the attempt time. Exhausted attempts or a
// context that ends between attempts return RestoreDrillMarkerError. That
// error identifies the marker write and supplies a distinct operator exit
// status instead of reporting a failed restore.
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
	failure := &RestoreDrillMarkerError{Err: putErr}
	slog.ErrorContext(ctx, "backup.restore_drill.failed",
		slog.String("err", failure.Error()),
		slog.Int("exit_status", restoreDrillMarkerExitStatus))
	return failure
}
