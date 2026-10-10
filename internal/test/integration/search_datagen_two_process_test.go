package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const twoProcessEngineMemoryBytes int64 = 8 << 30
const twoProcessTransactionTimeout = 5 * time.Second

// TestSearchDatagenTwoProcess requires the two-process check to pass against two graphs that share one fixture.
func TestSearchDatagenTwoProcess(t *testing.T) {
	cfg := newTwoProcessEngines(t)
	firstGraph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build first graph: %v", err)
	}
	t.Cleanup(firstGraph.Close)
	secondGraph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build second graph: %v", err)
	}
	t.Cleanup(secondGraph.Close)
	firstGraph.StartSearchWorkers(t.Context())
	first := httptest.NewServer(twoProcessHandler(firstGraph))
	t.Cleanup(first.Close)
	second := httptest.NewServer(twoProcessHandler(secondGraph))
	t.Cleanup(second.Close)
	for _, endpoints := range []string{"ftp://a.example,http://b.example", "http://a.example", "http://,http://b.example"} {
		if output, err := runDatagenSearchCommand(t, cfg, "--commit", "--endpoints", endpoints); err == nil || len(output) != 0 {
			t.Fatalf("search with endpoints %q: got %v; output=%s", endpoints, err, output)
		}
	}
	if err := datagen.VerifySearchTwoProcess(t.Context(), cfg, []string{first.URL, second.URL}, first.Client()); err != nil {
		t.Fatalf("two-process search: %v", err)
	}
	if err := datagen.VerifySearchTwoProcess(t.Context(), cfg, []string{first.URL}, first.Client()); err == nil {
		t.Fatal("one endpoint passed the two-process check")
	}
}

// twoProcessHandler serves the authenticated MCP handler of graph for POST requests on /mcp and /mcp/.
func twoProcessHandler(graph *appruntime.Graph) http.Handler {
	handler := graph.AuthMiddleware(graph.MCPHandler)
	mux := http.NewServeMux()
	mux.Handle("POST /mcp", handler)
	mux.Handle("POST /mcp/", handler)
	return mux
}

// newTwoProcessEngines sets the search environment, isolates the FoundationDB key space, and serves a new index behind the public alias.
func newTwoProcessEngines(t *testing.T) *config.Config {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	engine := testenv.OpenSearchWithMemory(t, twoProcessEngineMemoryBytes)
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, []byte(engine.CA), 0o600); err != nil {
		t.Fatalf("write OpenSearch CA: %v", err)
	}
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		t.Fatalf("generate cursor key: %v", err)
	}
	for name, value := range map[string]string{
		"DATABASE_URL": testenv.Ledger(t), "FDB_CLUSTER_FILE": cluster, "ENV": "production",
		"OPENSEARCH_ENDPOINT": engine.Endpoint, "OPENSEARCH_CA": caPath, "OPENSEARCH_USERNAME": engine.Username,
		"OPENSEARCH_PASSWORD": engine.Password, "OPENSEARCH_PAGE_BYTES": "128", "OPENSEARCH_WORKER_IDLE_INTERVAL": "50ms",
		"OPENSEARCH_SHARDS": "1", "OPENSEARCH_ROUTING_SHARDS": "24", "OPENSEARCH_REPLICAS": "0",
		"OPENSEARCH_PUBLIC_ENABLED": "true", "OPENSEARCH_CURSOR_KEY": base64.StdEncoding.EncodeToString(cursorKey),
		"OPENSEARCH_RESPONSE_MAX_BYTES": "16384", "AUTH_MEMBERSHIP_CACHE_LIFETIME": "0s", "AUTH_TOKEN_CACHE_LIFETIME": "0s",
	} {
		t.Setenv(name, value)
	}
	prefix := []byte("search-two-process-test:" + uuid.Must(uuid.NewV7()).String() + ":")
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearTwoProcessPrefix(t, cluster, prefix) })
	stores, err := fdbadapter.NewStores(cluster, twoProcessTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open search stores: %v", err)
	}
	stores.EnableSearchWork()
	settings := search.Config{Endpoint: engine.Endpoint, CA: engine.CA, Username: engine.Username, Password: engine.Password, RequestTimeout: 2 * time.Minute, MaxRetries: 1}
	adapter, err := search.New(t.Context(), settings)
	if err != nil {
		t.Fatalf("open search adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.WithoutCancel(t.Context())) })
	baseClient, err := opensearch.NewClient(settings.ClientConfig(nil))
	if err != nil {
		t.Fatalf("open OpenSearch client: %v", err)
	}
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	t.Cleanup(func() {
		request := opensearchapi.IndicesDeleteReq{Indices: []string{index}}
		_, _ = opensearchapi.NewFromClient(baseClient).Indices.Delete(context.WithoutCancel(t.Context()), request)
	})
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 24, Replicas: 0}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatalf("create search index: %v", err)
	}
	if err := adapter.SetAlias(t.Context(), search.PublicAlias, index); err != nil {
		t.Fatalf("set public alias: %v", err)
	}
	if err := stores.InitializeSearchIndex(t.Context(), index); err != nil {
		t.Fatalf("record serving search index: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.DatagenAllowTarget, cfg.AuditKafkaBrokers, cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "local", "", cfg.DatabaseURL, false
	return cfg
}

// clearTwoProcessPrefix range-clears the test key space and removes the prefix.
func clearTwoProcessPrefix(t *testing.T, cluster string, prefix []byte) {
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(cluster, twoProcessTransactionTimeout)
	if err != nil {
		t.Logf("cleanup: open fdb: %v", err)
		return
	}
	end := append(bytes.Clone(prefix), 0xFF)
	if _, err := database.Transact(func(transaction fdb.Transaction) (any, error) {
		transaction.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	}); err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}
