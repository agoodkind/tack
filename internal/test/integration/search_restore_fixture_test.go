package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
)

func restoreQueryFixture(t *testing.T, fixture *queryFixture, cluster string) {
	t.Helper()
	stores, err := fdbadapter.NewStores(cluster, testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open restored cluster: %v", err)
	}
	stores.EnableSearchWork()
	configuration := *fixture.Config
	configuration.FDBClusterFile = cluster
	graph := buildQueryGraph(t, &configuration)
	fixture.Stores = stores
	fixture.Config = &configuration
	fixture.Graph = graph
	fixture.Harness = processHarness(fixture.Harness, graph)
	fixture.Worker = newSearchWorker(t, stores, fixture.Adapter, clock.Wall{}, searchWorkerSettings(runtimePageBytes))
}

func requireRestoredSnapshot(t *testing.T, fixture queryFixture, orgID, edited, deleted, created uuid.UUID) {
	t.Helper()
	if current, err := fixture.Stores.Nodes.Get(t.Context(), orgID, deleted); err != nil || current == nil {
		t.Fatalf("restore did not recover the node deleted after backup: %v", err)
	}
	current, err := fixture.Stores.Nodes.Get(t.Context(), orgID, created)
	if err != nil {
		t.Fatalf("read postbackup node: %v", err)
	}
	if current != nil {
		t.Fatalf("restore included postbackup node %s", created)
	}
	current, err = fixture.Stores.Nodes.Get(t.Context(), orgID, edited)
	if err != nil || current == nil {
		t.Fatalf("read restored edited node: %v", err)
	}
	for _, value := range current.Props {
		var text string
		if json.Unmarshal(value, &text) == nil && text == "postbackup changed text" {
			t.Fatal("restored node contains the postbackup edit")
		}
	}
	t.Logf("restored snapshot recovered deleted=%s reverted edited=%s excluded created=%s", deleted, edited, created)
}
