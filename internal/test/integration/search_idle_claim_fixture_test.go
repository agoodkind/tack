package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	idleClaimTransactionTimeout = 5 * time.Second
	idleClaimNodeText           = "idle claim text"
	idleClaimRoutingShards      = 8
)

type idleClaimNode struct {
	OrgID       uuid.UUID
	NodeID      uuid.UUID
	TypeKey     string
	IncludedKey string
}

func newIdleClaimStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	prefix := []byte("search-idle-claim-test:" + uuid.Must(uuid.NewV7()).String() + ":")
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearIdleClaimPrefix(t, cluster, prefix) })
	stores, err := fdbadapter.NewStores(cluster, idleClaimTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open search stores: %v", err)
	}
	stores.EnableSearchWork()
	return stores
}

func clearIdleClaimPrefix(t *testing.T, cluster string, prefix []byte) {
	t.Helper()
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(cluster, idleClaimTransactionTimeout)
	if err != nil {
		t.Logf("cleanup: open FoundationDB: %v", err)
		return
	}
	end := append(bytes.Clone(prefix), 0xFF)
	if _, err := database.Transact(func(transaction fdb.Transaction) (any, error) {
		transaction.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	}); err != nil {
		t.Logf("cleanup: clear the test prefix: %v", err)
	}
}

func newIdleClaimWorkStore(t *testing.T, stores *fdbadapter.Stores) *fdbadapter.SearchWorkStore {
	t.Helper()
	if err := stores.InitializeSearchIndex(integration.WithTestOperation(t, t.Context()), "idle-claim-"+uuid.Must(uuid.NewV7()).String()); err != nil {
		t.Fatalf("record serving index: %v", err)
	}
	return stores.SearchWork(clock.Wall{})
}

func idleClaimKey(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

func putIdleClaimNode(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID) idleClaimNode {
	t.Helper()
	fixture := idleClaimNode{OrgID: orgID, NodeID: uuid.Must(uuid.NewV7()), TypeKey: idleClaimKey("n"), IncludedKey: idleClaimKey("p")}
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: fixture.IncludedKey, Type: node.PropertyTypeText,
		Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := stores.PropertyDefs.Set(integration.WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("store property definition: %v", err)
	}
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: fixture.TypeKey, TypeKey: fixture.TypeKey,
		Slug: fixture.TypeKey, PluralSlug: fixture.TypeKey + "s",
		PropertyDefIDs: []uuid.UUID{definition.ID}, Features: node.Features{node.FeatureIsEntryPoint},
	}
	if err := stores.NodeTypes.Set(integration.WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	writeIdleClaimNode(t, stores, fixture)
	return fixture
}

func writeIdleClaimNode(t *testing.T, stores *fdbadapter.Stores, fixture idleClaimNode) {
	t.Helper()
	text, err := json.Marshal(idleClaimNodeText)
	if err != nil {
		t.Fatalf("encode node text: %v", err)
	}
	props := map[string]json.RawMessage{fixture.IncludedKey: text}
	now := clock.Now().UTC()
	value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: idleClaimNodeText, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: idleClaimNodeText, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(integration.WithTestOperation(t, t.Context()), value, view); err != nil {
		t.Fatalf("store node %s: %v", fixture.NodeID, err)
	}
}

// scheduleCopyAndRebuild schedules rebuild and copy work and returns the
// copied node's ID. It writes the node through stores without calling
// [fdbadapter.Stores.EnableSearchWork]. The node does not have pending live
// work. Copy scheduling skips nodes with pending live work.
// The function begins an index replacement, claims the rebuild work,
// advances the replacement to copying, and runs one copy scheduling step.
// That step schedules copy work for the node. A copy item is claimable only
// while the replacement is copying or verifying. The function then yields
// the rebuild claim. The rebuild work remains pending and claimable after
// the yield.
func scheduleCopyAndRebuild(t *testing.T, stores *fdbadapter.Stores, store *fdbadapter.SearchWorkStore, source idleClaimNode) uuid.UUID {
	t.Helper()
	unscheduled, err := fdbadapter.NewStores(testenv.FoundationDB(t), idleClaimTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores without search work: %v", err)
	}
	copied := source
	copied.NodeID = uuid.Must(uuid.NewV7())
	writeIdleClaimNode(t, unscheduled, copied)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	if _, err := rebuilds.BeginRebuild(integration.WithTestOperation(t, t.Context()), searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: idleClaimRoutingShards, Replicas: 0, Restored: false, Reason: "idle claim test",
	}); err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	work, claimed, err := store.Claim(integration.WithTestOperation(t, t.Context()), searchdomain.WorkClassRebuild, idleClaimOwner, idleClaimLease)
	if err != nil || !claimed {
		t.Fatalf("claim replacement work: claimed %t, error %v", claimed, err)
	}
	creating := currentIdleClaimRebuild(t, rebuilds)
	copying := creating
	copying.State = searchdomain.RebuildCopying
	if err := rebuilds.AdvanceRebuild(integration.WithTestOperation(t, t.Context()), work, creating, copying); err != nil {
		t.Fatalf("advance replacement to copying: %v", err)
	}
	if err := rebuilds.CopyNextNodes(integration.WithTestOperation(t, t.Context()), work, currentIdleClaimRebuild(t, rebuilds)); err != nil {
		t.Fatalf("schedule replacement copies: %v", err)
	}
	if err := store.Yield(integration.WithTestOperation(t, t.Context()), work); err != nil {
		t.Fatalf("yield replacement claim: %v", err)
	}
	return copied.NodeID
}

func currentIdleClaimRebuild(t *testing.T, rebuilds *fdbadapter.SearchRebuildStore) searchdomain.Rebuild {
	t.Helper()
	rebuild, found, err := rebuilds.CurrentRebuild(integration.WithTestOperation(t, t.Context()))
	if err != nil || !found {
		t.Fatalf("read index replacement: found %t, error %v", found, err)
	}
	return rebuild
}
