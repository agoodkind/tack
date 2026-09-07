package ops

import (
	"context"
	"time"
)

// nowFunc is the source of wall-clock time inside the ops package. Tests
// can override it to get deterministic timestamps; production code reads
// wall time through opsNow.
var nowFunc = time.Now

// opsNow returns the current wall-clock time. All backup and restore
// code that needs a timestamp calls this instead of [time.Now] directly.
func opsNow() time.Time {
	return nowFunc()
}

// waitFunc is the source of pauses inside the ops package. Tests override it
// to record the pause instead of taking it; production code pauses through
// opsWait.
var waitFunc = waitUntil

// opsWait pauses for d or until ctx ends, whichever comes first, and reports
// whether the whole pause was taken. False means the context ended it.
func opsWait(ctx context.Context, d time.Duration) bool {
	return waitFunc(ctx, d)
}

func waitUntil(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
