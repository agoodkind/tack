// Package integration tests the real FDB cluster, generic NodeService,
// resolver, and seed. internal/testenv starts a single-node FoundationDB
// cluster and migrated YugabyteDB ledger through the Docker SDK.
// Tests skip only under go test -short.
// Each test uses a separate FDB key prefix. A cleanup hook deletes keys
// under that prefix after the test finishes.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
)

// testTransactionTimeout bounds product-store transactions and retries using
// the deployment's timeout for stalled store operations.
const testTransactionTimeout = 5 * time.Second

// TestEnv supplies isolated test dependencies.
type TestEnv struct {
	Stores  *fdbadapter.Stores
	Ops     *ops.Env
	NodeSvc *service.NodeService
	Seeder  *service.Seeder
	OrgID   uuid.UUID
	OrgSlug string
	Ctx     context.Context
}

// writeOrgNode writes the root org and its read records directly through
// stores. The root uses uuid.Nil for ParentID and sets OrgID equal to ID.
func writeOrgNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	orgID uuid.UUID,
	slug string,
) error {
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
	// CreateAtomic writes the slug's node_by_property index in this transaction.
	if err := stores.Nodes.CreateAtomic(ctx, n, view, nil, []string{"slug"}, nil, nil); err != nil {
		slog.ErrorContext(ctx, "node.seed.create_failed", slog.String("err", err.Error()))
		return fmt.Errorf("create org node %s: %w", orgID, err)
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

// clearPrefix deletes every key under a test prefix in one FDB transaction.
// It opens an independent database handle for cleanup after stores close.
func clearPrefix(t *testing.T, clusterFile string, prefix []byte) {
	t.Helper()
	db, err := fdbadapter.Open(clusterFile, testTransactionTimeout)
	if err != nil {
		t.Logf("cleanup: open fdb: %v", err)
		return
	}

	end := append([]byte{}, prefix...)
	end = append(end, 0xFF)
	_, err = db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{
			Begin: fdb.Key(prefix),
			End:   fdb.Key(end),
		})

		return nil, nil
	})
	if err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}

// SetupTestEnv creates isolated test dependencies.
func SetupTestEnv(t *testing.T) *TestEnv {
	t.Helper()
	clusterFile := testenv.FoundationDB(t)
	ledgerDSN := testenv.Ledger(t)

	prefix := uuid.New()
	prefixBytes := append([]byte("tack-test:"), prefix[:]...)
	fdbadapter.SetTestPrefix(prefixBytes)

	stores, err := fdbadapter.NewStores(clusterFile, testTransactionTimeout, nil)
	if err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("open fdb: %v", err)
	}

	ctx := WithTestOperation(t, context.Background())
	pool, err := postgres.NewPool(ctx, ledgerDSN, nil)
	if err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("open postgres: %v", err)
	}

	// Close the pool even when setup fails to avoid exhausting connection limits.
	t.Cleanup(pool.Close)

	// Use a stable org UUID within each test's separate FDB key prefix.
	const seedSlug = "test-org"
	orgID := uuid.MustParse("019e6b4a-0000-7000-8000-000000000001")

	seeder := service.NewSeeder(stores.PropertyDefs, stores.NodeTypes)
	if err := seeder.SeedOrg(ctx, orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	// SeedOrg writes types and property definitions, not the org node.
	// Child creates require that node to resolve their parent's org.
	if err := writeOrgNode(ctx, stores, orgID, seedSlug); err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("write org node: %v", err)
	}

	t.Cleanup(func() {
		clearPrefix(t, clusterFile, prefixBytes)
		fdbadapter.SetTestPrefix(nil)
	})

	svc, err := service.NewNodeService(
		stores.Nodes,
		stores.Views,
		stores.NodeTypes,
		stores.PropertyDefs,
		stores.Relationships,
		stores.NodeDeleter,
		postgres.NewOrgMemberRepo(pool),
		stores.Relationships,
	)
	if err != nil {
		t.Fatalf("build the node service: %v", err)
	}

	return &TestEnv{
		Stores:  stores,
		Ops:     &ops.Env{Cfg: nil, Pool: pool, Stores: stores, Log: slog.Default()},
		NodeSvc: svc,
		Seeder:  seeder,
		OrgID:   orgID,
		OrgSlug: seedSlug,
		Ctx:     ctx,
	}
}
