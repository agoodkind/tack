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

// RestorableVersionsFromDescribe reads the restorable window of `fdbbackup
// describe` output with the parser retention uses. restorable is false while
// the describe does not report the backup as restorable.
func RestorableVersionsFromDescribe(describe string) (minVersion, maxVersion int64, restorable bool, err error) {
	if !describeReportsRestorable(describe) {
		return 0, 0, false, nil
	}
	window, err := fdbRestorableVersionsFromDescribe(describe)
	return window.MinVersion, window.MaxVersion, true, err
}
