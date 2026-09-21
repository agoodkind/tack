package integration

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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
	handler   http.Handler
	token     string
	ledgerDSN string
	orgID     uuid.UUID
	Workspace string
	Project   string
}

// harnessConfig loads the server configuration for the test engines that
// testenv starts for this process.
func harnessConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", testenv.Ledger(t))
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
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

// NewMCPHarness builds the runtime graph against the test engines and
// bootstraps a fresh org, workspace, and project for the calling test.
func NewMCPHarness(t *testing.T) *MCPHarness {
	return newMCPHarness(t, false)
}

// NewAuditedMCPHarness builds the same public MCP boundary with the real
// Yugabyte audit writer enabled.
func NewAuditedMCPHarness(t *testing.T) *MCPHarness {
	return newMCPHarness(t, true)
}

func newMCPHarness(t *testing.T, audited bool) *MCPHarness {
	t.Helper()
	ctx := t.Context()
	cfg := harnessConfig(t)
	cfg.AuditKafkaBrokers = ""
	if audited {
		cfg.AuditWriterDSN = cfg.DatabaseURL
		cfg.AuditAllowUnrecorded = false
		cfg.AuditReadFlushInterval = 10 * time.Millisecond
	} else {
		cfg.AuditWriterDSN = ""
		cfg.AuditAllowUnrecorded = true
	}
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
		handler:   graph.AuthMiddleware(graph.MCPHandler),
		token:     workspace.Actors[0].Token,
		ledgerDSN: cfg.DatabaseURL,
		orgID:     workspace.OrgID,
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
