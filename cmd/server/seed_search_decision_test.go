package main

import (
	"bytes"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// TestSeedCommandStoresSearchDecisionOnEverySeededDefinition runs `seed`
// through the audited command tree against the test ledger, the real
// operator outbox, and the test FoundationDB. Every definition the command
// stores for the seeded organization must declare search inclusion or
// exclusion when read back from FoundationDB.
func TestSeedCommandStoresSearchDecisionOnEverySeededDefinition(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	clusterFile := testenv.FoundationDB(t)
	suffix := uuid.NewString()[:8]
	email := "seed-search-" + suffix + "@example.test"
	t.Setenv("DATABASE_URL", ledgerDSN)
	t.Setenv("FDB_CLUSTER_FILE", clusterFile)
	t.Setenv("SEED_EMAIL", email)
	t.Setenv("SEED_NAME", "Seed Search Test")
	t.Setenv("SEED_ORG_SLUG", "seed-search-"+suffix)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	isolateSeedKeys(t, cfg, []byte("seed-search-test:"+suffix+":"))
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)

	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	root := buildRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "seed", "--allow-reseed",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("seed: %v\n%s", err, output.String())
	}

	orgIDs, err := postgres.NewOrgMemberRepo(pool).ListOrgIDsForUser(t.Context(), userIDForEmail(email))
	if err != nil || len(orgIDs) != 1 {
		t.Fatalf("seeded organizations of %s = %v, %v, want one", email, orgIDs, err)
	}
	stores, err := fdbadapter.NewStores(clusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open the stores: %v", err)
	}
	definitions, err := stores.PropertyDefs.List(t.Context(), orgIDs[0])
	if err != nil || len(definitions) == 0 {
		t.Fatalf("seeded definitions = %d, %v, want at least one", len(definitions), err)
	}
	for _, definition := range definitions {
		if definition.Search == nil {
			t.Fatalf("the seed stored definition %s without a search decision", definition.Name)
		}
	}
	t.Logf("the seed stored %d definitions, each with a search decision", len(definitions))
}

// isolateSeedKeys points every store of this process at prefix and clears
// the prefix when the test ends.
func isolateSeedKeys(t *testing.T, cfg *config.Config, prefix []byte) {
	t.Helper()
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		defer fdbadapter.SetTestPrefix(nil)
		database, err := fdbadapter.Open(cfg.FDBClusterFile, cfg.FDBTransactionTimeout)
		if err != nil {
			t.Errorf("open the database to clear %s: %v", prefix, err)
			return
		}
		keyRange, err := fdb.PrefixRange(prefix)
		if err != nil {
			t.Errorf("create the range of %s: %v", prefix, err)
			return
		}
		if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
			tr.ClearRange(keyRange)
			return nil, nil
		}); err != nil {
			t.Errorf("clear %s: %v", prefix, err)
		}
	})
}
