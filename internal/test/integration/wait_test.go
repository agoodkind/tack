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

// waitUntil waits in real time until the wall clock passes instant.
func waitUntil(t *testing.T, instant time.Time) {
	t.Helper()
	timeout := instant.Sub(clock.Now()) + 2*waitForInterval
	if !waitFor(t, timeout, func() bool { return clock.Now().After(instant) }) {
		t.Fatalf("the test ended before %s", instant.UTC().Format(time.RFC3339Nano))
	}
}
