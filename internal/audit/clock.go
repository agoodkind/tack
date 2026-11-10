package audit

import "time"

// nowFunc supplies wall-clock time for the audit package. Tests can replace it
// for deterministic timestamps. Production code reads it through
// monoStart and sinceMs.
var nowFunc = time.Now

// sinceMs returns the elapsed milliseconds since start as a float64.
func sinceMs(start time.Time) float64 {
	return float64(nowFunc().Sub(start)) / float64(time.Millisecond)
}

// monoStart returns a wall-clock starting point suitable for sinceMs.
func monoStart() time.Time {
	return nowFunc()
}
