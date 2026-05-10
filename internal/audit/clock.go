package audit

import "time"

// nowFunc is the source of wall-clock time inside the audit package. Tests
// override it via the helper in export_test.go when they need deterministic
// timestamps; production code reads it through monoStart and sinceMs.
var nowFunc = time.Now

// sinceMs returns the elapsed milliseconds since start as a float64.
func sinceMs(start time.Time) float64 {
	return float64(nowFunc().Sub(start)) / float64(time.Millisecond)
}

// monoStart returns a wall-clock starting point suitable for sinceMs.
func monoStart() time.Time {
	return nowFunc()
}
