package integration

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/testenv"
)

const (
	// outageRecoveryDeadline bounds engine readiness and model deployment
	// after the container restarts.
	outageRecoveryDeadline = 5 * time.Minute
	// outageSearchDeadline bounds how long the committed node may take to
	// become searchable after the engine and model are ready.
	outageSearchDeadline = 10 * time.Second
	outagePhrase         = "vermilion outage beacon"
)

// setEngineRunning stops or starts the OpenSearch container of the fixture.
// testenv.OpenSearchWithMemory returns one shared engine for each memory
// limit, and newQueryFixture uses the engine for nativeSearchMemoryBytes.
// The endpoint address stays assigned while the container is stopped. A stop
// logs the exit code and duration, and fails the test when the engine did not
// exit on SIGTERM within testenv.OpenSearchStopTimeout.
func setEngineRunning(t *testing.T, running bool) {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	ctx := context.WithoutCancel(WithTestOperation(t, t.Context()))
	if !running {
		stop, err := testenv.StopOpenSearch(ctx, engine)
		t.Logf("engine %s stopped with exit code %d after %s", engine.Container, stop.ExitCode, stop.Duration)
		if err != nil {
			t.Fatalf("stop engine %s: %v", engine.Container, err)
		}
		return
	}
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = docker.Close() }()
	if _, err := docker.ContainerStart(ctx, engine.Container, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start engine %s: %v", engine.Container, err)
	}
}

// TestSearchDatagenOutage stops the engine, commits a node through MCP, and
// requires search to return an explicit error instead of an empty result.
// After the engine and model are ready again, the node must become
// searchable within ten seconds.
func TestSearchDatagenOutage(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	// drainSearchWork indexes the fixture's bootstrapped nodes before the
	// engine stops to exclude fixture setup from the search wait after restart.
	drainSearchWork(t, fixture.Worker, 2000)
	setEngineRunning(t, false)
	t.Cleanup(func() { setEngineRunning(t, true) })

	identifier := "OUTAGE" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	created := fixture.Harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: fixture.Harness.Workspace, Name: outagePhrase,
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	nodeID, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created node id %q: %v", created.RawID(), err)
	}
	if page, err := trySearch(fixture.Harness, outagePhrase, ""); err == nil {
		t.Fatalf("search during the outage returned a page with %d results instead of an error", len(page.IDs))
	}

	startEngine(t, fixture)
	deadline := time.Now().Add(outageSearchDeadline)
	for time.Now().Before(deadline) {
		_, _ = fixture.Worker.RunSlice(WithTestOperation(t, t.Context()))
		if page, err := trySearch(fixture.Harness, outagePhrase, ""); err == nil && slices.Contains(page.IDs, nodeID) {
			return
		}
	}
	t.Fatalf("node %s committed during the outage was not searchable within %s of recovery", nodeID, outageSearchDeadline)
}

func startEngine(t *testing.T, fixture queryFixture) {
	t.Helper()
	setEngineRunning(t, true)
	recovery := time.Now().Add(outageRecoveryDeadline)
	for {
		if _, err := fixture.Adapter.Provision(WithTestOperation(t, t.Context())); err == nil {
			return
		}
		if time.Now().After(recovery) {
			engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
			diagnostics := testenv.OpenSearchDiagnostics(context.WithoutCancel(WithTestOperation(t, t.Context())), engine)
			t.Fatalf("the engine and model were not ready within %s: %s", outageRecoveryDeadline, diagnostics)
		}
		time.Sleep(time.Second)
	}
}

// TestSearchUnavailableWhileEngineStopped verifies tack_search returns the unavailable response while the engine is stopped and a result after the engine starts.
func TestSearchUnavailableWhileEngineStopped(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	arguments := searchArguments(fixture.Harness, outagePhrase, "")
	setEngineRunning(t, false)
	t.Cleanup(func() { setEngineRunning(t, true) })

	text, isError, err := rawSearchCall(fixture.Harness, arguments)
	if err != nil {
		t.Fatalf("call tack_search during the outage: %v", err)
	}
	if !isError || text != unavailableSearchMessage {
		t.Fatalf("tack_search did not return the unavailable response during the outage: isError=%t text %q, want %q", isError, text, unavailableSearchMessage)
	}

	startEngine(t, fixture)
	deadline := time.Now().Add(outageSearchDeadline)
	for {
		text, isError, err = rawSearchCall(fixture.Harness, arguments)
		if err == nil && !isError {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tack_search did not succeed after the engine started: isError=%t text %q err %v within %s", isError, text, err, outageSearchDeadline)
		}
		time.Sleep(time.Second)
	}
}

// `requireSearchReturnsNode` allows asynchronous indexing to finish before reporting a missing search result.
func requireSearchReturnsNode(t *testing.T, fixture queryFixture, nodeID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(outageSearchDeadline)
	for time.Now().Before(deadline) {
		_, _ = fixture.Worker.RunSlice(WithTestOperation(t, t.Context()))
		if page, err := trySearch(fixture.Harness, outagePhrase, ""); err == nil && slices.Contains(page.IDs, nodeID) {
			return
		}
	}
	t.Fatalf("search did not return the node before the deadline: node %s within %s", nodeID, outageSearchDeadline)
}

// `TestSearchUnavailableWhileModelUndeployed` requires an unavailable response while the pinned model is undeployed and a search result containing the node after redeployment.
func TestSearchUnavailableWhileModelUndeployed(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	arguments := searchArguments(fixture.Harness, outagePhrase, "")
	identifier := "MODEL" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	created := fixture.Harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: fixture.Harness.Workspace, Name: outagePhrase,
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	nodeID, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("failed to parse created node identifier: %q: %v", created.RawID(), err)
	}
	// drainSearchWork runs worker slices until the worker cannot claim any
	// search work. The fixture's bootstrapped nodes and the new project finish
	// indexing before the ten-second search deadline starts.
	drainSearchWork(t, fixture.Worker, 2000)
	requireSearchReturnsNode(t, fixture, nodeID)
	model, err := fixture.Adapter.Provision(WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatalf("failed to read pinned model: %v", err)
	}
	undeployNativeModel(t, fixture.Adapter, fixture.Client, model.ID)

	text, isError, err := rawSearchCall(fixture.Harness, arguments)
	if err != nil {
		t.Fatalf("search call failed while pinned model was undeployed: %v", err)
	}
	if !isError || text != unavailableSearchMessage {
		t.Fatalf("search did not return the unavailable response while pinned model was undeployed: isError=%t text %q, want %q", isError, text, unavailableSearchMessage)
	}

	if err := redeployNativeModel(WithTestOperation(t, t.Context()), fixture.Adapter, fixture.Client); err != nil {
		t.Fatalf("failed to redeploy pinned model: %v", err)
	}
	requireSearchReturnsNode(t, fixture, nodeID)
}
