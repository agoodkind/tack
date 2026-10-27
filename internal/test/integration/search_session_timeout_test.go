package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// sessionTimeoutQuery is the query text of every search in the session
	// timeout test.
	sessionTimeoutQuery = "slate harbor session timeout"
	// sessionTimeoutUnavailableText is the public tack_search response for an
	// unavailable search backend.
	sessionTimeoutUnavailableText = "Search is temporarily unavailable."
	// sessionTimeoutHoldLimit bounds the wait for the predict request and the
	// point in time create request to arrive at the proxy.
	sessionTimeoutHoldLimit = time.Minute
	// sessionTimeoutPause is how long FoundationDB stays paused after the
	// proxy forwards the point in time create response. The session create
	// transaction starts right after that response and times out after its
	// 5 s limit. The expired-session sweep then starts a transaction with
	// another 5 s limit. The test resumes FoundationDB before that second
	// limit ends.
	sessionTimeoutPause = 7500 * time.Millisecond
	// sessionTimeoutRecovery bounds how long a new search may keep failing
	// after FoundationDB resumes.
	sessionTimeoutRecovery = 30 * time.Second
)

// TestSearchSessionTimeoutReturnsUnavailable pauses the FoundationDB
// container while the proxy keeps the predict request of a new search open,
// after the search has finished its earlier FoundationDB reads. The proxy
// then forwards the predict request, and the session create transaction
// times out with FoundationDB error 1031. tack_search must return the
// unavailable text. After FoundationDB resumes, a new search must succeed.
func TestSearchSessionTimeoutReturnsUnavailable(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	harness, proxy := newPredictHoldHarness(t, fixture)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	proxy.arm()

	outcome := make(chan error, 1)
	go func() {
		_, err := integration.TrySearch(harness, sessionTimeoutQuery, "")
		outcome <- err
	}()
	waitForSignal(t, proxy.held, "predict request")
	paused := true
	setContainerPaused(t, container, true)
	t.Cleanup(func() {
		if paused {
			setContainerPaused(t, container, false)
		}
	})
	proxy.release()
	waitForSignal(t, proxy.pitCreated, "point in time create request")
	time.Sleep(sessionTimeoutPause)
	setContainerPaused(t, container, false)
	paused = false

	if err := <-outcome; err == nil || err.Error() != sessionTimeoutUnavailableText {
		t.Fatalf("tack_search with a timed out session create returned error %v, want %q", err, sessionTimeoutUnavailableText)
	}
	deadline := clock.Now().Add(sessionTimeoutRecovery)
	for {
		_, err := integration.TrySearch(harness, sessionTimeoutQuery, "")
		if err == nil {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("tack_search did not succeed within %s after FoundationDB resumed: %v", sessionTimeoutRecovery, err)
		}
		time.Sleep(time.Second)
	}
}

// waitForSignal waits until signal closes and fails the test after
// sessionTimeoutHoldLimit.
func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(sessionTimeoutHoldLimit):
		t.Fatalf("the proxy did not receive the %s within %s", name, sessionTimeoutHoldLimit)
	}
}

// setContainerPaused pauses or resumes the named container.
func setContainerPaused(t *testing.T, container string, paused bool) {
	t.Helper()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = docker.Close() }()
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	if paused {
		_, err = docker.ContainerPause(ctx, container, client.ContainerPauseOptions{})
	} else {
		_, err = docker.ContainerUnpause(ctx, container, client.ContainerUnpauseOptions{})
	}
	if err != nil {
		t.Fatalf("set container %s paused %t: %v", container, paused, err)
	}
}
