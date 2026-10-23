package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// rolloutEngineMemory sets the minimum memory per guest for the search
	// model on the shared engine.
	rolloutEngineMemory int64 = 8 << 30
	// rolloutTransactionTimeout bounds every product store transaction and
	// matches the deployed default.
	rolloutTransactionTimeout = 5 * time.Second
	// rolloutPageBytes sets the page size for the fixture worker and runtime.
	// This small size gives each node several pages.
	rolloutPageBytes = 128
)

// rolloutFixture uses one production graph with real FoundationDB,
// OpenSearch, SQL authentication, bootstrapped organizations, and a
// production search worker.
type rolloutFixture struct {
	stores     *fdbadapter.Stores
	adapter    *search.Adapter
	client     *opensearchapi.Client
	engine     testenv.OpenSearchFixture
	index      string
	cfg        *config.Config
	handler    http.Handler
	workspaces []datagen.WorkspaceIdentity
	worker     *service.SearchWorker
}

// newRolloutFixture provisions the model and one serving index behind the
// public alias. It records the index in FoundationDB, builds the
// production graph, and bootstraps identities.
func newRolloutFixture(t *testing.T) rolloutFixture {
	t.Helper()
	stores := newRolloutStores(t)
	engine := testenv.OpenSearchWithMemory(t, rolloutEngineMemory)
	adapter, client := rolloutSearchClients(t, engine)
	model, err := adapter.Provision(rolloutContext(t))
	if err != nil {
		t.Fatalf("cannot provision search model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	deleteRolloutIndex(t, client, index)
	t.Cleanup(func() { deleteRolloutIndex(t, client, index) })
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 24, Replicas: 0}
	if err := adapter.EnsureIndex(rolloutContext(t), index, spec); err != nil {
		t.Fatalf("cannot create search index: %v", err)
	}
	if err := adapter.SetAlias(rolloutContext(t), search.PublicAlias, index); err != nil {
		t.Fatalf("cannot set public alias: %v", err)
	}
	if err := stores.InitializeSearchIndex(rolloutContext(t), index); err != nil {
		t.Fatalf("cannot record serving index: %v", err)
	}
	cfg := rolloutConfig(t, engine)
	graph, err := appruntime.BuildGraph(rolloutContext(t), cfg)
	if err != nil {
		t.Fatalf("cannot build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	scale, err := datagen.ParseScale("medium")
	if err != nil {
		t.Fatalf("cannot parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(rolloutContext(t), cfg, clock.Now().UnixNano(), scale)
	if err != nil {
		t.Fatalf("cannot bootstrap identities: %v", err)
	}
	return rolloutFixture{
		stores: stores, adapter: adapter, client: client, engine: engine, index: index, cfg: cfg,
		handler: graph.AuthMiddleware(graph.MCPHandler), workspaces: identities.Workspaces,
		worker: newRolloutWorker(t, stores, adapter),
	}
}

// rolloutContext returns the test context with a new audit operation for the
// integration test system actor using integration.WithTestOperation.
func rolloutContext(t *testing.T) context.Context {
	t.Helper()
	return integration.WithTestOperation(t, t.Context())
}

// newRolloutStores opens search stores under a FoundationDB key prefix
// for one test and clears the prefix after the test.
func newRolloutStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	prefix := append([]byte("rollout-test:"), uuid.Must(uuid.NewV7()).String()...)
	prefix = append(prefix, ':')
	fdbadapter.SetTestPrefix(prefix)
	stores, err := fdbadapter.NewStores(cluster, rolloutTransactionTimeout, nil)
	if err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("cannot open search stores: %v", err)
	}
	t.Cleanup(func() {
		clearRolloutPrefix(t, cluster, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	stores.EnableSearchWork()
	return stores
}

// clearRolloutPrefix clears every key under the test prefix in one
// transaction.
func clearRolloutPrefix(t *testing.T, cluster string, prefix []byte) {
	t.Helper()
	database, err := fdbadapter.Open(cluster, rolloutTransactionTimeout)
	if err != nil {
		t.Errorf("cannot open FoundationDB for cleanup: %v", err)
		return
	}
	end := append(append([]byte{}, prefix...), 0xFF)
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	}); err != nil {
		t.Errorf("cannot clear test prefix: %v", err)
	}
}

// rolloutSearchClients returns the production adapter and a typed
// official client with the same settings.
func rolloutSearchClients(t *testing.T, engine testenv.OpenSearchFixture) (*search.Adapter, *opensearchapi.Client) {
	t.Helper()
	configuration := rolloutSearchConfig(engine)
	adapter, err := search.New(rolloutContext(t), configuration)
	if err != nil {
		t.Fatalf("cannot open search client: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.WithoutCancel(t.Context())) })
	client, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		t.Fatalf("cannot open search client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return adapter, opensearchapi.NewFromClient(client)
}

// rolloutSearchConfig returns the production adapter settings for the
// engine.
func rolloutSearchConfig(engine testenv.OpenSearchFixture) search.Config {
	return search.Config{
		Endpoint: engine.Endpoint, CA: engine.CA, Username: engine.Username,
		Password: engine.Password, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	}
}

// deleteRolloutIndex deletes the index and its aliases and accepts an
// absent index.
func deleteRolloutIndex(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	response, err := opensearch.Do[json.RawMessage](ctx, client.Client, http.MethodDelete,
		opensearchapi.IndicesDeleteReq{Indices: []string{index}}, nil)
	if err != nil {
		t.Errorf("cannot delete index %s: %v", index, err)
		return
	}
	if response.IsError() && response.StatusCode != http.StatusNotFound {
		t.Errorf("cannot delete index %s: %v", index, opensearch.ParseError(response))
	}
}
