package integration_test

import (
	"strings"
	"testing"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchDatagenMissingReaderRefusesBeforeFixtures calls the audited
// command with real FoundationDB and SQL stores. A missing reader must
// prevent every FoundationDB fixture write before search verification.
func TestSearchDatagenMissingReaderRefusesBeforeFixtures(t *testing.T) {
	seedlessStores(t)
	cfg := integration.QueryConfig(t)
	cfg.DatagenAllowTarget = "local"
	cfg.SearchPublicEnabled = true
	database, err := fdbadapter.Open(cfg.FDBClusterFile, prefixCopyTimeout)
	if err != nil {
		t.Fatalf("open the fixture database: %v", err)
	}
	keyRange := testPrefixKeys(t)
	if before := readTestKeys(t, database, keyRange); len(before) != 0 {
		t.Fatalf(
			"the isolated fixture prefix starts with %d keys, want an empty prefix",
			len(before),
		)
	}
	for _, dsn := range []string{"", " \t "} {
		cfg.AuditReaderDSN = dsn
		output, err := runDatagenSearchCommand(t, cfg, "--commit")
		if err == nil || !strings.Contains(
			err.Error(),
			"the ledger verb report needs AUDIT_READER_DSN",
		) {
			t.Fatalf("committed search without a reader returned %v; output=%s", err, output)
		}
		if after := readTestKeys(t, database, keyRange); len(after) != 0 {
			t.Fatalf("the refused command created %d FoundationDB fixture keys", len(after))
		}
	}
	if output, err := runDatagenSearchCommand(t, cfg); err != nil {
		t.Fatalf("search dry run without a reader returned %v; output=%s", err, output)
	}
}
