package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"github.com/spf13/cobra"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/ops"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const (
	cohortEngineMemoryBytes int64 = 8 << 30
	cohortTransactionTimeout      = 5 * time.Second
)

// TestDatagenSearchVerifyCohort runs --verify-cohort through the audited
// command while the production search worker loops of a second graph index
// the cohort.
func TestDatagenSearchVerifyCohort(t *testing.T) {
	cfg := newCohortEngines(t)
	workers, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build worker graph: %v", err)
	}
	t.Cleanup(workers.Close)
	workers.StartSearchWorkers(t.Context())
	output := runCohortCommand(t, cfg, clock.Now().UnixNano())
	var envelope struct {
		Result struct {
			Verified bool                             `json:"verified"`
			Manifest datagen.SearchManifest           `json:"manifest"`
			Cohort   datagen.SearchCohortVerification `json:"cohort"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatalf("decode cohort result: %v; output=%s", err, output)
	}
	cohort := envelope.Result.Cohort
	if !envelope.Result.Verified || !cohort.Verified || !cohort.WaitCompleted || len(cohort.Cases) != len(envelope.Result.Manifest.Cases) {
		t.Fatalf("cohort verification differs: %+v", cohort)
	}
	for _, result := range cohort.Cases {
		if !result.Passed {
			t.Fatalf("cohort case %q failed: %+v", result.Query, result)
		}
		t.Logf("cohort case query=%q match=%s ranks=%v returned=%d wait=%s", result.Query, result.Match, result.Ranks, result.Returned, cohort.WaitDuration)
	}
}

// newCohortEngines sets the production search environment, isolates the
// FoundationDB key space, and serves one new index behind the public alias.
func newCohortEngines(t *testing.T) *config.Config {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	engine := testenv.OpenSearchWithMemory(t, cohortEngineMemoryBytes)
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
	prefix := []byte("search-cohort-test:" + uuid.Must(uuid.NewV7()).String() + ":")
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearCohortPrefix(t, cluster, prefix) })
	stores, err := fdbadapter.NewStores(cluster, cohortTransactionTimeout, nil)
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

// runCohortCommand runs the audited command and returns its JSON output.
func runCohortCommand(t *testing.T, cfg *config.Config, seed int64) []byte {
	t.Helper()
	var output bytes.Buffer
	factory := cli.System(cfg)
	factory.Out = &output
	pool, err := pgxpool.New(t.Context(), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Search Test",
		"ops", "qa", "datagen", "search", "--verify-cohort", "--commit", "--seed", strconv.FormatInt(seed, 10),
	})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("verify cohort: %v; output=%s", err, output.String())
	}
	return output.Bytes()
}

// clearCohortPrefix range-clears the test key space and removes the prefix.
func clearCohortPrefix(t *testing.T, cluster string, prefix []byte) {
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(cluster, cohortTransactionTimeout)
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
