package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// unresponsiveQuery is the query text of every search in the paused-engine test.
	unresponsiveQuery = "amber stalled beacon"
	// unresponsiveUnavailableText is the public tack_search response for an unavailable engine.
	unresponsiveUnavailableText = "Search is temporarily unavailable."
	// unresponsiveRecoveryDeadline bounds how long a search may keep failing after the engine
	// resumes.
	unresponsiveRecoveryDeadline = 30 * time.Second
)

// setEnginePaused pauses or resumes the shared OpenSearch container of the query fixture.
// A paused engine keeps its listening socket. The kernel accepts each connection, and the
// frozen engine sends no response.
func setEnginePaused(t *testing.T, paused bool) {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, integration.NativeSearchMemoryBytes)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = docker.Close() }()
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	if paused {
		_, err = docker.ContainerPause(ctx, engine.Container, client.ContainerPauseOptions{})
	} else {
		_, err = docker.ContainerUnpause(ctx, engine.Container, client.ContainerUnpauseOptions{})
	}
	if err != nil {
		t.Fatalf("set engine %s paused %t: %v", engine.Container, paused, err)
	}
}

// TestSearchUnavailableWhileEngineUnresponsive pauses the engine container and calls
// tack_search. Each engine request waits until its timeout or the search request deadline
// ends it. tack_search must return the unavailable response instead of an unexpected server
// error. After the engine resumes, a search must succeed.
func TestSearchUnavailableWhileEngineUnresponsive(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	paused := true
	setEnginePaused(t, true)
	t.Cleanup(func() {
		if paused {
			setEnginePaused(t, false)
		}
	})

	started := time.Now()
	_, err := integration.TrySearch(fixture.Harness, unresponsiveQuery, "")
	if err == nil || err.Error() != unresponsiveUnavailableText {
		t.Fatalf("tack_search with the engine paused returned error %v, want %q", err, unresponsiveUnavailableText)
	}
	t.Logf("tack_search returned the unavailable response %s after the engine paused", time.Since(started))

	setEnginePaused(t, false)
	paused = false
	deadline := time.Now().Add(unresponsiveRecoveryDeadline)
	for {
		_, err = integration.TrySearch(fixture.Harness, unresponsiveQuery, "")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			engine := testenv.OpenSearchWithMemory(t, integration.NativeSearchMemoryBytes)
			diagnostics := testenv.OpenSearchDiagnostics(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), engine)
			t.Fatalf("tack_search did not succeed within %s after the engine resumed: %v: %s", unresponsiveRecoveryDeadline, err, diagnostics)
		}
		time.Sleep(time.Second)
	}
}
