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
// The endpoint address stays assigned while the container is stopped.
func setEngineRunning(t *testing.T, running bool) {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("open docker client: %v", err)
	}
	defer func() { _ = docker.Close() }()
	ctx := context.WithoutCancel(t.Context())
	if running {
		_, err = docker.ContainerStart(ctx, engine.Container, client.ContainerStartOptions{})
	} else {
		_, err = docker.ContainerStop(ctx, engine.Container, client.ContainerStopOptions{})
	}
	if err != nil {
		t.Fatalf("set engine %s running %t: %v", engine.Container, running, err)
	}
}

// TestSearchDatagenOutage stops the engine, commits a node through MCP, and
// requires search to return an explicit error instead of an empty result.
// After the engine and model are ready again, the node must become
// searchable within ten seconds.
func TestSearchDatagenOutage(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
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
		_, _ = fixture.Worker.RunSlice(t.Context())
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
		if _, err := fixture.Adapter.Provision(t.Context()); err == nil {
			return
		}
		if time.Now().After(recovery) {
			t.Fatalf("the engine and model were not ready within %s", outageRecoveryDeadline)
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
		_, _ = fixture.Worker.RunSlice(t.Context())
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
	requireSearchReturnsNode(t, fixture, nodeID)
	model, err := fixture.Adapter.Provision(t.Context())
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

	if err := redeployNativeModel(t.Context(), fixture.Adapter, fixture.Client); err != nil {
		t.Fatalf("failed to redeploy pinned model: %v", err)
	}
	requireSearchReturnsNode(t, fixture, nodeID)
}
