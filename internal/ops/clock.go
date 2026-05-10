package ops

import "time"

// nowFunc is the source of wall-clock time inside the ops package. Tests
// can override it to get deterministic timestamps; production code reads
// wall time through opsNow.
var nowFunc = time.Now

// opsNow returns the current wall-clock time. All backup and restore
// code that needs a timestamp calls this instead of [time.Now] directly.
func opsNow() time.Time {
	return nowFunc()
}
