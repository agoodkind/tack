package ops

import "testing"

// backupS3Attempts sets the S3 client's retry budget until the test ends, and
// restores the budget the test found. Callers pass 1 while the client points at
// an address that refuses connections, either the unreachable host the alarm
// tests configure or the object store engine a test has stopped. The refusal is
// the answer those tests assert on, and the client's remaining attempts add
// only its jittered backoff before repeating it: about ten seconds per
// staleness run, which was most of this package's test time (TACK-528).
//
// Clients already built keep the budget they were built with, so a caller sets
// this before the client it means to bound is created. RunBackupStalenessCheck
// builds its own client on every run, so a budget set before the run bounds it.
func backupS3Attempts(t *testing.T, attempts int) {
	t.Helper()
	previous := backupS3MaxAttempts
	backupS3MaxAttempts = attempts
	t.Cleanup(func() { backupS3MaxAttempts = previous })
}
