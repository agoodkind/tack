package ops

import (
	"testing"
	"time"
)

// SetBackupRetentionTestClock selects the existing wall-clock hook for a
// serial retention acceptance test and restores it during test cleanup.
func SetBackupRetentionTestClock(t testing.TB, now time.Time) {
	t.Helper()
	previous := nowFunc
	//testseam:external The system wall clock supplies the retention cutoff.
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = previous })
}
