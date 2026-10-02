package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/testenv"
)

const (
	// clusterPhrase is the included text of every node the cluster tests write.
	clusterPhrase = "cobalt proxy lantern"
	// clusterDeadline bounds each wait: model deployment, green health, a
	// failed write, pending work, and a node becoming searchable.
	clusterDeadline = 5 * time.Minute
	clusterPoll     = 250 * time.Millisecond
	// clusterSearchPages bounds the tack_search pages one attempt reads.
	clusterSearchPages = 100
)

// newClusterQueryFixture builds the newQueryFixture graph over cluster. The
// adapter, the worker, and the graph use the proxy as their only endpoint.
func newClusterQueryFixture(t *testing.T, cluster *testenv.OpenSearchCluster) (queryFixture, search.IndexSpec) {
	t.Helper()
	stores := newSearchStore(t)
	adapter, client := openSearchClientsFor(t, cluster.Fixture)
	index := "cluster-" + uuid.Must(uuid.NewV7()).String()
	spec := search.IndexSpec{Model: clusterProvision(t, adapter), MappingVersion: "1", Primaries: 1, RoutingShards: 24, Replicas: 0}
	clusterRequire(t, "create search index", adapter.EnsureIndex(t.Context(), index, spec))
	clusterRequire(t, "set public alias", adapter.SetAlias(t.Context(), search.PublicAlias, index))
	clusterRequire(t, "record serving index", stores.InitializeSearchIndex(t.Context(), index))
	caPath := filepath.Join(t.TempDir(), "search-ca.crt")
	clusterRequire(t, "write search CA", os.WriteFile(caPath, []byte(cluster.Fixture.CA), 0o600))
	secret := cluster.Fixture.Password
	t.Setenv("OPENSEARCH_ENDPOINT", cluster.Fixture.Endpoint)
	t.Setenv("OPENSEARCH_CA", caPath)
	t.Setenv("OPENSEARCH_USERNAME", cluster.Fixture.Username)
	t.Setenv("OPENSEARCH_PASSWORD", secret)
	t.Setenv("OPENSEARCH_PAGE_BYTES", strconv.Itoa(runtimePageBytes))
	t.Setenv("OPENSEARCH_SHARDS", strconv.Itoa(spec.Primaries))
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", strconv.Itoa(spec.RoutingShards))
	t.Setenv("OPENSEARCH_REPLICAS", strconv.Itoa(spec.Replicas))
	configureQueryEnvironment(t, defaultQueryOptions())
	cfg := queryConfig(t)
	graph := buildQueryGraph(t, cfg)
	scale, err := datagen.ParseScale("medium")
	clusterRequire(t, "parse scale", err)
	seed := nextHarnessSeed()
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
	clusterRequire(t, "bootstrap identities", err)
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
	}, spec
}

func clusterRequire(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
}

// clusterFailure returns nil for a nil err and otherwise a retry reason that
// starts with operation.
func clusterFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	return errors.New(operation + ": " + err.Error())
}

// clusterEventually retries check until it returns nil and fails the test
// with the last error after clusterDeadline.
func clusterEventually(t *testing.T, operation string, check func() error) {
	t.Helper()
	deadline := clock.Now().Add(clusterDeadline)
	for err := check(); err != nil; err = check() {
		if clock.Now().After(deadline) {
			t.Fatalf("%s within %s: %v", operation, clusterDeadline, err)
		}
		time.Sleep(clusterPoll)
	}
}

// clusterProvision registers and deploys the pinned model without node IDs,
// retrying until the engine answers.
func clusterProvision(t *testing.T, adapter *search.Adapter) search.ModelInfo {
	t.Helper()
	var model search.ModelInfo
	clusterEventually(t, "provision the search model", func() error {
		provisioned, err := adapter.Provision(t.Context())
		model = provisioned
		return clusterFailure("provision", err)
	})
	return model
}

// clusterNodeWriter stores one opaque node type in the first workspace. The
// returned function creates one node of that type with clusterPhrase through
// the production create path, which records durable search work.
func clusterNodeWriter(t *testing.T, fixture queryFixture) func() uuid.UUID {
	t.Helper()
	workspace := fixture.Workspaces[0]
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	parent := entryPoint(t, fixture, workspace)
	return func() uuid.UUID {
		return putOpaqueNode(t, fixture, kind, parent, "cluster node", clusterPhrase, "excluded cluster text")
	}
}

func requireSearchable(t *testing.T, fixture queryFixture, expected []uuid.UUID, onFailure ...clusterFailureCallbacks) {
	t.Helper()
	clusterEventually(t, "search every written node", func() error {
		for range 100 {
			claimed, err := fixture.Worker.RunSlice(t.Context())
			if err != nil {
				for _, diagnose := range onFailure {
					diagnose.Worker(err)
				}
				return clusterFailure("run search worker slice", err)
			}
			if !claimed {
				break
			}
		}
		found, cursor, complete := []uuid.UUID{}, "", false
		for range clusterSearchPages {
			page, err := trySearch(fixture.Harness, clusterPhrase, cursor)
			if err != nil {
				for _, diagnose := range onFailure {
					diagnose.Public(err)
				}
				return err
			}
			found = append(found, page.IDs...)
			complete = page.Complete
			if complete {
				break
			}
			cursor = page.Cursor
		}
		if !complete {
			return fmt.Errorf("search results continue past %d pages", clusterSearchPages)
		}
		for _, nodeID := range expected {
			if !slices.Contains(found, nodeID) {
				return fmt.Errorf("node %s is not in the search results", nodeID)
			}
		}
		return nil
	})
}

// requireModelOnEveryMember reads the model record through the ML Commons
// API until it reports a deployment to all nodes with one worker per member.
func requireModelOnEveryMember(t *testing.T, fixture queryFixture, modelID string, members int) {
	t.Helper()
	clusterEventually(t, "deploy the model to every member", func() error {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/_plugins/_ml/models/"+url.PathEscape(modelID), nil)
		clusterRequire(t, "build the model request", err)
		response, err := fixture.Client.Client.Perform(request)
		if err != nil {
			return clusterFailure("read the model", err)
		}
		defer func() { _ = response.Body.Close() }()
		var model struct {
			AllNodes bool `json:"deploy_to_all_nodes"`
			Workers  int  `json:"current_worker_node_count"`
		}
		if err := json.NewDecoder(response.Body).Decode(&model); err != nil {
			return clusterFailure("decode the model", err)
		}
		if !model.AllNodes || model.Workers != members {
			return fmt.Errorf("model deploy_to_all_nodes=%t with %d workers, want true with %d", model.AllNodes, model.Workers, members)
		}
		return nil
	})
}
