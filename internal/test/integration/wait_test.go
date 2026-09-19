package integration

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
)

const waitForInterval = 200 * time.Millisecond

// waitFor retries check every 200 ms until it returns true or timeout passes,
// for state that another process makes visible asynchronously.
func waitFor(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()
	deadline := clock.Now().Add(timeout)
	for {
		if check() {
			return true
		}
		if clock.Now().After(deadline) {
			return false
		}
		select {
		case <-t.Context().Done():
			return false
		case <-time.After(waitForInterval):
		}
	}
}
