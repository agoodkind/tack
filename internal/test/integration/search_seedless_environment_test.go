package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	seedlessTransactionTimeout = 5 * time.Second
	seedlessPageBytes          = 4096
	seedlessResponseBytes      = 16384
	seedlessRoutingShards      = 24
	seedlessCursorKeyBytes     = 32
)

// seedlessEnvironment groups a production graph and search worker over real
// FoundationDB, OpenSearch, and SQL ledger services. Its serving index backs
// the public alias. The test clears its FoundationDB key prefix during
// cleanup.
type seedlessEnvironment struct {
	Stores  *fdbadapter.Stores
	Adapter *search.Adapter
	Index   string
	Graph   *appruntime.Graph
	Worker  *service.SearchWorker
	Pool    *pgxpool.Pool
}

func newSeedlessEnvironment(t *testing.T) seedlessEnvironment {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, integration.NativeSearchMemoryBytes)
	stores := seedlessStores(t)
	adapter := seedlessAdapter(t, engine)
	index := seedlessIndex(t, stores, adapter)
	configureSeedlessSearch(t, engine)
	cfg := integration.QueryConfig(t)
	graph, err := appruntime.BuildGraph(integration.WithTestOperation(t, t.Context()), cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	pool, err := postgres.NewPool(integration.WithTestOperation(t, t.Context()), cfg.DatabaseURL, nil)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return seedlessEnvironment{
		Stores: stores, Adapter: adapter, Index: index, Graph: graph,
		Worker: seedlessWorker(t, stores, adapter), Pool: pool,
	}
}

func seedlessStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	prefix := []byte("search-seedless-test:" + uuid.Must(uuid.NewV7()).String() + ":")
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearSeedlessPrefix(t, cluster, prefix) })
	stores, err := fdbadapter.NewStores(cluster, seedlessTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open search stores: %v", err)
	}
	stores.EnableSearchWork()
	return stores
}

func clearSeedlessPrefix(t *testing.T, cluster string, prefix []byte) {
	t.Helper()
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(cluster, seedlessTransactionTimeout)
	if err != nil {
		t.Errorf("open FoundationDB to clear %s: %v", prefix, err)
		return
	}
	keys, err := fdb.PrefixRange(prefix)
	if err != nil {
		t.Errorf("create the range of %s: %v", prefix, err)
		return
	}
	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Errorf("create the transaction that clears %s: %v", prefix, err)
		return
	}
	transaction.ClearRange(keys)
	if err := transaction.Commit().Get(); err != nil {
		t.Errorf("clear %s: %v", prefix, err)
	}
}

func seedlessAdapter(t *testing.T, engine testenv.OpenSearchFixture) *search.Adapter {
	t.Helper()
	secret := engine.Password
	adapter, err := search.New(integration.WithTestOperation(t, t.Context()), search.Config{
		Endpoint: engine.Endpoint, CA: engine.CA, Username: engine.Username, Password: secret,
		RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("create the search adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(integration.WithTestOperation(t, t.Context()))); err != nil {
			t.Errorf("close the search adapter: %v", err)
		}
	})
	return adapter
}

func seedlessIndex(t *testing.T, stores *fdbadapter.Stores, adapter *search.Adapter) string {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	model, err := adapter.Provision(ctx)
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	spec := search.IndexSpec{
		Model: model, MappingVersion: search.MappingVersion, Primaries: 1,
		RoutingShards: seedlessRoutingShards, Replicas: 0,
	}
	if err := adapter.EnsureIndex(ctx, index, spec); err != nil {
		t.Fatalf("create search index: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.DeleteIndex(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), index); err != nil {
			t.Errorf("delete search index %s: %v", index, err)
		}
	})
	if err := adapter.SetAlias(ctx, search.PublicAlias, index); err != nil {
		t.Fatalf("set public alias: %v", err)
	}
	if err := stores.InitializeSearchIndex(ctx, index); err != nil {
		t.Fatalf("record serving search index: %v", err)
	}
	return index
}

func configureSeedlessSearch(t *testing.T, engine testenv.OpenSearchFixture) {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, []byte(engine.CA), 0o600); err != nil {
		t.Fatalf("write OpenSearch CA: %v", err)
	}
	cursorKey := make([]byte, seedlessCursorKeyBytes)
	if _, err := rand.Read(cursorKey); err != nil {
		t.Fatalf("generate cursor key: %v", err)
	}
	integration.ConfigureSearchMaintenance(t, engine)
	secret := engine.Password
	settings := map[string]string{
		"OPENSEARCH_ENDPOINT": engine.Endpoint, "OPENSEARCH_CA": caPath,
		"OPENSEARCH_USERNAME": engine.Username, "OPENSEARCH_PASSWORD": secret,
		"OPENSEARCH_PAGE_BYTES": strconv.Itoa(seedlessPageBytes), "OPENSEARCH_WORKER_IDLE_INTERVAL": "50ms",
		"OPENSEARCH_SHARDS": "1", "OPENSEARCH_ROUTING_SHARDS": strconv.Itoa(seedlessRoutingShards),
		"OPENSEARCH_REPLICAS": "0", "ENV": "production", "OPENSEARCH_PUBLIC_ENABLED": "true",
		"OPENSEARCH_CURSOR_KEY":          base64.StdEncoding.EncodeToString(cursorKey),
		"OPENSEARCH_RESPONSE_MAX_BYTES":  strconv.Itoa(seedlessResponseBytes),
		"AUTH_MEMBERSHIP_CACHE_LIFETIME": "0s", "AUTH_TOKEN_CACHE_LIFETIME": "0s",
	}
	for name, value := range settings {
		t.Setenv(name, value)
	}
}

func seedlessWorker(t *testing.T, stores *fdbadapter.Stores, adapter *search.Adapter) *service.SearchWorker {
	t.Helper()
	source := clock.Wall{}
	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(source, policies)
	settings := config.SearchWorkerSettings{
		PageBytes: seedlessPageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: 2 * time.Second,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: map[string]int{"live": 1, "access": 1, "cleanup": 1, "rescan": 1, "rollout": 1, "rebuild": 1, "copy": 1},
		ReplacementPauseLimit: config.SearchReplacementPauseLimit, WriteCompletionHorizon: config.SearchWriteCompletionHorizon,
	}
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: stores.SearchWork(source), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: adapter,
		Rollouts: rollouts, Sessions: rollouts, Documents: adapter,
		Rebuilds: stores.SearchRebuilds(source), Replacer: adapter, Audit: appruntime.SearchWorkerAudit(stores),
	}, source, settings, 0)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}
	return worker
}
