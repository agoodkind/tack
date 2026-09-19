package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

// MCPHarness calls MCP tools through the production HTTP handler with a real
// bearer token, so each call runs auth, org membership, audit, and rendering.
type MCPHarness struct {
	driver    *datagen.Driver
	token     string
	Workspace string
	Project   string
}

// unreachableMeiliURL points config.Load at an address nothing serves. The
// runtime graph falls back to its no-op searcher when Meilisearch is
// unreachable, and the harness exercises the MCP tools, not search.
const unreachableMeiliURL = "http://[::1]:1"

// harnessConfig loads the server configuration against this process's test
// engines, started through testenv.
func harnessConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	t.Setenv("MEILI_URL", unreachableMeiliURL)
	t.Setenv("MEILI_MASTER_KEY", "tack-test-unused-key")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// harnessSeedCounter mints a distinct seed for every harness. Bootstrap
// identities derives the org ID and workspace slug deterministically from
// the seed, so a fresh seed per call gives each NewMCPHarness call its own
// isolated org and workspace, even when a test builds more than one harness.
var harnessSeedCounter = seededCounter()

func seededCounter() *atomic.Int64 {
	counter := &atomic.Int64{}
	counter.Store(clock.Now().UnixNano())
	return counter
}

func nextHarnessSeed() int64 {
	return harnessSeedCounter.Add(1)
}

// NewMCPHarness builds the runtime graph against the test stack and
// bootstraps a fresh org, workspace, and project for the calling test.
func NewMCPHarness(t *testing.T) *MCPHarness {
	t.Helper()
	ctx := t.Context()
	cfg := harnessConfig(t)
	// The test ledger has no audit roles. The harness exercises MCP tool
	// behavior, not the audit pipeline, so the graph runs unrecorded.
	cfg.AuditWriterDSN = ""
	cfg.AuditAllowUnrecorded = true
	graph, err := appruntime.BuildGraph(ctx, cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	seed := nextHarnessSeed()
	identities, err := datagen.BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	workspace := identities.Workspaces[0]
	harness := &MCPHarness{
		driver:    datagen.NewDriver(graph, false, seed),
		token:     workspace.Actors[0].Token,
		Workspace: workspace.Slug,
		Project:   "",
	}
	harness.createProject(t)
	return harness
}

// createProject creates one project in the harness's workspace through
// tack_create_project, addressed by a unique identifier derived from the
// test name, and records it as the harness's project reference.
func (h *MCPHarness) createProject(t *testing.T) {
	t.Helper()
	identifier := strings.ReplaceAll(t.Name(), "/", "-")
	args := datagen.ToolArguments{
		WorkspaceReference: h.Workspace,
		ProjectReference:   "",
		IssueReference:     "",
		Name:               identifier,
		Properties: datagen.NodeProperties{
			"identifier": json.RawMessage(strconv.Quote(identifier)),
		},
		NodeID:       "",
		Query:        "",
		NodeType:     "",
		Direction:    "",
		SourceID:     "",
		RelationType: "",
		TargetID:     "",
	}
	h.Call(t, "tack_create_project", args)
	h.Project = identifier
}

// Call invokes one MCP tool through the harness's bearer token and fails the
// test when the call returns an error.
func (h *MCPHarness) Call(t *testing.T, toolName string, args datagen.ToolArguments) datagen.Result {
	t.Helper()
	result, err := h.driver.Call(t.Context(), h.token, toolName, args)
	if err != nil {
		t.Fatalf("call %s: %v", toolName, err)
	}
	return result
}

// CallExpectError invokes one MCP tool and fails the test when it succeeds,
// returning the error text so the caller can assert on it.
func (h *MCPHarness) CallExpectError(t *testing.T, toolName string, args datagen.ToolArguments) string {
	t.Helper()
	_, err := h.driver.Call(t.Context(), h.token, toolName, args)
	if err == nil {
		t.Fatalf("call %s: expected an error, got success", toolName)
	}
	return err.Error()
}
