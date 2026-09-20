package ops

import "testing"

// backupS3Attempts bounds the retry budget of clients built after it, until the
// test ends. Callers pass 1 against a refusing address: the retries only repeat
// the refusal after the SDK's backoff, about ten seconds per staleness run
// (TACK-528).
func backupS3Attempts(t *testing.T, attempts int) {
	t.Helper()
	previous := backupS3MaxAttempts
	backupS3MaxAttempts = attempts
	t.Cleanup(func() { backupS3MaxAttempts = previous })
}
