package ops

import "testing"

// backupS3Attempts sets the attempt limit for clients built afterward. Test
// cleanup restores the previous limit.
func backupS3Attempts(t *testing.T, attempts int) {
	t.Helper()
	previous := backupS3MaxAttempts
	backupS3MaxAttempts = attempts
	t.Cleanup(func() { backupS3MaxAttempts = previous })
}
