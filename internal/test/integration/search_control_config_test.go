package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// searchControlConfig returns the production configuration the audited search
// commands read. It uses the fixture endpoint and CA and one primary shard.
// The commands connect to the ledger through DatabaseURL and to FoundationDB
// through FDBClusterFile. The readiness check reads property definitions from
// FoundationDB.
func searchControlConfig(t *testing.T, fixture testenv.OpenSearchFixture) *config.Config {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "search-ca.pem")
	if err := os.WriteFile(caPath, []byte(fixture.CA), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "8")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	pass := fixture.Password
	return &config.Config{
		SearchEndpoint: fixture.Endpoint, SearchCA: caPath,
		SearchUsername: fixture.Username, SearchPassword: pass,
		SearchRequestTimeout: 2 * time.Minute, SearchMaxRetries: 1,
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t),
		FDBTransactionTimeout: testTransactionTimeout,
	}
}
