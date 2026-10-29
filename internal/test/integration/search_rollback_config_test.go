package integration_test

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// rolloutConfig sets the production search and query environment for the
// engine and loads the server configuration.
func rolloutConfig(t *testing.T, engine testenv.OpenSearchFixture) *config.Config {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, []byte(engine.CA), 0o600); err != nil {
		t.Fatalf("cannot write CA file: %v", err)
	}
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		t.Fatalf("cannot generate cursor key: %v", err)
	}
	settings := map[string]string{
		"OPENSEARCH_ENDPOINT": engine.Endpoint, "OPENSEARCH_CA": caPath, "OPENSEARCH_USERNAME": engine.Username,
		"OPENSEARCH_PASSWORD": engine.Password, "OPENSEARCH_PAGE_BYTES": strconv.Itoa(rolloutPageBytes), "OPENSEARCH_WORKER_IDLE_INTERVAL": "50ms",
		"OPENSEARCH_SHARDS": "1", "OPENSEARCH_ROUTING_SHARDS": "24", "OPENSEARCH_REPLICAS": "0",
		"ENV": "production", "OPENSEARCH_PUBLIC_ENABLED": "true",
		"OPENSEARCH_CURSOR_KEY": base64.StdEncoding.EncodeToString(cursorKey), "OPENSEARCH_RESPONSE_MAX_BYTES": "16384",
		"AUTH_MEMBERSHIP_CACHE_LIFETIME": "0s", "AUTH_TOKEN_CACHE_LIFETIME": "0s",
		"DATABASE_URL": testenv.Ledger(t), "FDB_CLUSTER_FILE": testenv.FoundationDB(t),
	}
	for name, value := range settings {
		t.Setenv(name, value)
	}
	integration.ConfigureSearchMaintenance(t, engine)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("cannot load configuration: %v", err)
	}
	cfg.AuditKafkaBrokers = ""
	cfg.AuditWriterDSN = cfg.DatabaseURL
	cfg.AuditAllowUnrecorded = false
	cfg.AuditReadFlushInterval = 10 * time.Millisecond
	return cfg
}

func newRolloutWorker(t *testing.T, stores *fdbadapter.Stores, adapter *search.Adapter) *service.SearchWorker {
	t.Helper()
	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(clock.Wall{}, policies)
	settings := config.SearchWorkerSettings{
		PageBytes: rolloutPageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: 2 * time.Second,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: map[string]int{"live": 1, "access": 1, "cleanup": 1, "rescan": 1, "rollout": 1, "rebuild": 1, "copy": 1},
		ReplacementPauseLimit: config.SearchReplacementPauseLimit, WriteCompletionHorizon: config.SearchWriteCompletionHorizon,
	}
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: stores.SearchWork(clock.Wall{}), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: adapter,
		Rollouts: rollouts, Sessions: rollouts, Documents: adapter,
		Rebuilds: stores.SearchRebuilds(clock.Wall{}), Replacer: adapter, Audit: appruntime.SearchWorkerAudit(stores),
	}, clock.Wall{}, settings, 0)
	if err != nil {
		t.Fatalf("cannot create search worker: %v", err)
	}
	return worker
}
