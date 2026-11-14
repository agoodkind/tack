// Restore drills record success in the object store after all legs pass.
// Marker writes retry independently to avoid repeating two successful restores
// after a temporary object-store failure.

package ops

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// restoreDrillMarkerAttempts limits marker write attempts before failure.
const restoreDrillMarkerAttempts = 5

// restoreDrillMarkerFirstWait starts exponential retry delays. Five attempts
// span about two minutes to tolerate brief faults without hiding an outage.
const restoreDrillMarkerFirstWait = 8 * time.Second

// restoreDrillMarkerExitStatus distinguishes marker failure from restore
// failure. The service can avoid restarting both successful restores.
const restoreDrillMarkerExitStatus = 3

// RestoreDrillMarkerError reports marker failure after every restore leg
// passed. Err records the last write failure.
type RestoreDrillMarkerError struct {
	Err error
}

// Error identifies the marker write as the failed step.
func (e *RestoreDrillMarkerError) Error() string {
	return "restore-drill: every leg passed but the rehearsal marker write failed: " + e.Err.Error()
}

// Unwrap returns the last write error.
func (e *RestoreDrillMarkerError) Unwrap() error { return e.Err }

// OperatorExitStatus returns the CLI status for marker failure. The distinct
// method avoids confusing this status with child-process ExitCode methods.
func (e *RestoreDrillMarkerError) OperatorExitStatus() int { return restoreDrillMarkerExitStatus }

// recordRestoreDrillRehearsal writes a dated success for the staleness check.
// A successful marker write forms part of the drill's success because an
// undated rehearsal cannot establish recent recovery coverage.
//
// Retries preserve the original pass time. Exhausted attempts or cancellation
// between attempts return RestoreDrillMarkerError with its marker-specific
// status rather than reporting a failed restore.
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

		slog.ErrorContext(
			ctx,
			"backup.restore_drill.marker_put_failed",
			slog.Int("attempt", attempt),
			slog.Int("attempts", restoreDrillMarkerAttempts),
			slog.String("err", putErr.Error()),
		)
		if attempt == restoreDrillMarkerAttempts {
			break
		}

		if !opsWait(ctx, wait) {
			slog.ErrorContext(
				ctx,
				"backup.restore_drill.marker_retry_canceled",
				slog.Int("attempt", attempt),
				slog.String("err", putErr.Error()),
			)
			break
		}

		wait *= 2
	}

	failure := &RestoreDrillMarkerError{Err: putErr}
	slog.ErrorContext(
		ctx,
		"backup.restore_drill.failed",
		slog.String("err", failure.Error()),
		slog.Int("exit_status", restoreDrillMarkerExitStatus),
	)

	return failure
}
