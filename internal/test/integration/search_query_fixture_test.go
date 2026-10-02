package integration

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
)

// queryFixture is one production graph over real FoundationDB, OpenSearch,
// and SQL authentication with bootstrapped identities in two organizations.
type queryFixture struct {
	Stores     *fdbadapter.Stores
	Adapter    *search.Adapter
	Client     *opensearchapi.Client
	Index      string
	Config     *config.Config
	Graph      *appruntime.Graph
	Harness    *MCPHarness
	Workspaces []datagen.WorkspaceIdentity
	Worker     *service.SearchWorker
}

type queryOptions struct {
	Public        bool
	ResponseBytes int
	Primaries     int
}

func defaultQueryOptions() queryOptions {
	return queryOptions{Public: true, ResponseBytes: 16384, Primaries: 1}
}

// newQueryFixture provisions the pinned model and one serving index behind
// the public alias, records it in FoundationDB, and builds the production
// graph with real bearer validation and uncached membership.
func newQueryFixture(t *testing.T, options queryOptions) queryFixture {
	t.Helper()
	stores := newSearchStore(t)
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	// The engine refuses to auto-create node-pages-* indexes, as production does.
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	deleteNativeIndex(t, client, index)
	t.Cleanup(func() { deleteNativeIndex(t, client, index) })
	spec := search.IndexSpec{Model: model, MappingVersion: "1", Primaries: options.Primaries, RoutingShards: 24, Replicas: 0}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatalf("create search index: %v", err)
	}
	if err := adapter.SetAlias(t.Context(), search.PublicAlias, index); err != nil {
		t.Fatalf("set public alias: %v", err)
	}
	if err := stores.InitializeSearchIndex(t.Context(), index); err != nil {
		t.Fatalf("record serving search index: %v", err)
	}
	configureSearchRuntime(t)
	configureQueryEnvironment(t, options)
	cfg := queryConfig(t)
	graph := buildQueryGraph(t, cfg)
	scale, err := datagen.ParseScale("medium")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	seed := nextHarnessSeed()
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	workspace := identities.Workspaces[0]
	harness := &MCPHarness{
		driver: datagen.NewDriver(graph, false, seed), handler: graph.AuthMiddleware(graph.MCPHandler),
		token: workspace.Actors[0].Token, ledgerDSN: cfg.DatabaseURL, orgID: workspace.OrgID,
		Workspace: workspace.Slug, Project: "",
	}
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(runtimePageBytes))
	return queryFixture{
		Stores: stores, Adapter: adapter, Client: client, Index: index, Config: cfg,
		Graph: graph, Harness: harness, Workspaces: identities.Workspaces, Worker: worker,
	}
}

func configureQueryEnvironment(t *testing.T, options queryOptions) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate cursor key: %v", err)
	}
	t.Setenv("ENV", "production")
	t.Setenv("OPENSEARCH_PUBLIC_ENABLED", strconv.FormatBool(options.Public))
	t.Setenv("OPENSEARCH_CURSOR_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("OPENSEARCH_RESPONSE_MAX_BYTES", strconv.Itoa(options.ResponseBytes))
	t.Setenv("AUTH_MEMBERSHIP_CACHE_LIFETIME", "0s")
	t.Setenv("AUTH_TOKEN_CACHE_LIFETIME", "0s")
}

func queryConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := harnessConfig(t)
	cfg.AuditKafkaBrokers = ""
	cfg.AuditWriterDSN = cfg.DatabaseURL
	cfg.AuditAllowUnrecorded = false
	cfg.AuditReadFlushInterval = 10 * time.Millisecond
	return cfg
}

// buildQueryGraph builds one Tack process over the shared engines.
func buildQueryGraph(t *testing.T, cfg *config.Config) *appruntime.Graph {
	t.Helper()
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	return graph
}

// processHarness returns a harness for another process with the same token.
func processHarness(harness *MCPHarness, graph *appruntime.Graph) *MCPHarness {
	return &MCPHarness{
		driver: harness.driver, handler: graph.AuthMiddleware(graph.MCPHandler), token: harness.token,
		ledgerDSN: harness.ledgerDSN, orgID: harness.orgID, Workspace: harness.Workspace, Project: "",
	}
}

// entryPoint returns the entry node of one bootstrapped workspace. It finds
// the node through the entry-point type metadata of the organization.
func entryPoint(t *testing.T, fixture queryFixture, workspace datagen.WorkspaceIdentity) uuid.UUID {
	t.Helper()
	types, err := fixture.Stores.NodeTypes.List(t.Context(), workspace.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	for _, kind := range types {
		if !kind.Features.Has(node.FeatureIsEntryPoint) {
			continue
		}
		views, err := fixture.Stores.Views.List(t.Context(), node.NodeListQuery{OrgID: workspace.OrgID, NodeType: kind.TypeKey})
		if err != nil {
			t.Fatalf("list entry points: %v", err)
		}
		for _, view := range views {
			if view.Name == workspace.Name {
				return view.ID
			}
		}
	}
	t.Fatalf("workspace %s has no entry node", workspace.Slug)
	return uuid.Nil
}

// requireUnauthenticatedRefused requires the MCP boundary to refuse a call
// without a bearer token.
func requireUnauthenticatedRefused(t *testing.T, harness *MCPHarness) {
	t.Helper()
	anonymous := &MCPHarness{
		driver: harness.driver, handler: harness.handler, token: "", ledgerDSN: harness.ledgerDSN,
		orgID: harness.orgID, Workspace: harness.Workspace, Project: "",
	}
	status := rawCallStatus(t, anonymous, "tack_search", map[string]any{"query": "anything"})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated search returned HTTP %d", status)
	}
}
