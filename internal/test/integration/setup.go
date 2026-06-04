// Package integration holds end-to-end tests that exercise the real FDB
// cluster, the generic NodeService, the resolver, and the seed.
//
// Tests in this package are gated on the TACK_INTEGRATION environment
// variable. Without it, every test calls t.Skip. CI brings up FDB and
// postgres via docker-compose and sets TACK_INTEGRATION=1.
//
// Each test gets its own per-test FDB key prefix, set via
// foundationdb.SetTestPrefix at SetupTestEnv. After the test finishes a
// t.Cleanup hook range-clears every key under that prefix, so parallel tests
// on the same FDB cluster cannot collide.
package integration

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestEnv carries the dependencies a test needs to run a full create-then-resolve
// flow. It is built once per test by SetupTestEnv. The cleanup hook fires
// automatically via t.Cleanup; tests do not need to call anything manually.
type TestEnv struct {
	Stores  *fdbadapter.Stores
	NodeSvc *service.NodeService
	Seeder  *service.Seeder
	OrgID   uuid.UUID
	OrgSlug string
	Ctx     context.Context
}

// SetupTestEnv prepares an isolated TestEnv for a single test. It:
//   - skips the test when TACK_INTEGRATION is unset
//   - opens FDB via the cluster file in FDB_CLUSTER_FILE (default
//     /etc/foundationdb/fdb.cluster)
//   - assigns a per-test FDB key prefix derived from a fresh UUID
//   - runs Seeder.SeedOrg with the test orgID so NodeTypes and PropertyDefs
//     are written under the test prefix
//   - registers a t.Cleanup hook that range-clears the prefix
//
// The returned NodeSvc, Seeder, and Stores read and write only under the test
// prefix, so two parallel tests on the same FDB cluster never see each
// other's data.
func SetupTestEnv(t *testing.T) *TestEnv {
	t.Helper()
	if os.Getenv("TACK_INTEGRATION") == "" {
		t.Skip("integration test: set TACK_INTEGRATION=1 to run")
	}

	clusterFile := os.Getenv("FDB_CLUSTER_FILE")
	if clusterFile == "" {
		clusterFile = "/etc/foundationdb/fdb.cluster"
	}

	prefix := uuid.New()
	prefixBytes := append([]byte("tack-test:"), prefix[:]...)
	fdbadapter.SetTestPrefix(prefixBytes)

	stores, err := fdbadapter.NewStores(clusterFile, nil)
	if err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("open fdb: %v", err)
	}

	// Pin the org UUID to a known constant so integration tests produce stable,
	// repeatable IDs across runs. Different per-test FDB key prefixes keep
	// tests isolated even though they share the same orgID value.
	const seedSlug = "test-org"
	orgID := uuid.MustParse("019e6b4a-0000-7000-8000-000000000001")

	seeder := service.NewSeeder(stores.PropertyDefs, stores.NodeTypes)
	ctx := context.Background()
	seeder.SeedOrg(ctx, orgID)

	// Service.Create resolves a node's org by reading the parent's resolve
	// record. The seed only writes NodeTypes and PropertyDefs, so we also
	// need an org Node so child Create calls (workspace, project, etc.) can
	// resolve their parent. Mirrors cmd/server/seed.go ensureNode for the
	// root case.
	if err := writeOrgNode(ctx, stores, orgID, seedSlug); err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("write org node: %v", err)
	}

	t.Cleanup(func() {
		clearPrefix(t, clusterFile, prefixBytes)
		fdbadapter.SetTestPrefix(nil)
	})

	// Build NodeService against the prefixed stores. Some downstream calls
	// expect a non-nil searcher; pass a noop.
	svc := service.NewNodeService(
		stores.Nodes,
		stores.Views,
		stores.NodeTypes,
		stores.PropertyDefs,
		stores.Relationships,
		stores.NodeDeleter,
		noopSearcher{},
	)

	return &TestEnv{
		Stores:  stores,
		NodeSvc: svc,
		Seeder:  seeder,
		OrgID:   orgID,
		OrgSlug: seedSlug,
		Ctx:     ctx,
	}
}

// writeOrgNode writes the org Node, NodeView, NodeResolve, and slug index
// directly via the stores. It mirrors cmd/server/seed.go ensureNode for the
// root case, where ParentID is uuid.Nil and OrgID == ID.
func writeOrgNode(ctx context.Context, stores *fdbadapter.Stores, orgID uuid.UUID, slug string) error {
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"slug": mustJSON(slug),
	}
	n := &node.Node{
		ID:        orgID,
		OrgID:     orgID,
		NodeType:  "org",
		Name:      slug,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        orgID,
		OrgID:     orgID,
		NodeType:  "org",
		Name:      slug,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// CreateAtomic writes the node_by_property index for "slug" in the same
	// transaction, so no separate address write is needed.
	if err := stores.Nodes.CreateAtomic(ctx, n, view, nil, []string{"slug"}, nil); err != nil {
		return err
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// clearPrefix range-clears every key under a test prefix in one FDB
// transaction. It opens its own database handle so the cleanup remains
// independent of the test's stores.
func clearPrefix(t *testing.T, clusterFile string, prefix []byte) {
	t.Helper()
	db, err := fdbadapter.Open(clusterFile)
	if err != nil {
		t.Logf("cleanup: open fdb: %v", err)
		return
	}
	end := append([]byte{}, prefix...)
	end = append(end, 0xFF)
	_, err = db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	})
	if err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}
