//go:build runtimeaudit

package integration_test

import (
	"maps"
	"slices"
	"testing"

	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchRuntimeAuditCoverage runs successful audit cases for operations
// that require OpenSearch. It starts search workers and checks the recorded
// events when TACK_SEARCH_INTEGRATION=1 enables the search test engines.
func TestSearchRuntimeAuditCoverage(t *testing.T) {
	cfg := newCohortEngines(t)
	integration.ConfigureSearchMaintenance(t, testenv.OpenSearchWithMemory(t, cohortEngineMemoryBytes))
	server := newPublicServer(t, cfg, nil)
	server.graph.StartSearchWorkers(integration.WithTestOperation(t, t.Context()))
	env := &coverageEnv{
		server: server, ledger: startPublicLedger(t, server), admin: ledgerAdminPool(t),
		commandClasses: nil, nodes: nil, searchEnabled: true, backup: nil,
	}
	env.nodes = newCoverageFixture(t, env)
	cases := coverageCases()
	for _, key := range slices.Sorted(maps.Keys(failureOnlyCoverage)) {
		if failureOnlyCoverage[key] != coverageNeedsOpenSearch {
			continue
		}
		current, found := cases[key]
		if !found {
			t.Fatalf("The OpenSearch coverage entry requires an executable case, but coverageCases does not contain %q", key)
		}
		t.Run(key, func(t *testing.T) { env.runCase(t, current) })
	}
}
