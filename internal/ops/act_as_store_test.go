package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/testenv"
)

// actAsTransactionTimeout bounds every product-store transaction the act-as
// tests run. It matches the deployed default.
const actAsTransactionTimeout = 5 * time.Second

// actAsStores opens the FoundationDB stores under a per-test key prefix and
// clears every key under that prefix when the test ends.
func actAsStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	clusterFile := testenv.FoundationDB(t)
	prefixID := uuid.New()
	prefix := append([]byte("tack-test:"), prefixID[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearActAsPrefix(t, clusterFile, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	stores, err := fdbadapter.NewStores(clusterFile, actAsTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open foundationdb: %v", err)
	}
	return stores
}

// clearActAsPrefix range-clears every key under prefix on its own database
// handle.
func clearActAsPrefix(t *testing.T, clusterFile string, prefix []byte) {
	t.Helper()
	db, err := fdbadapter.Open(clusterFile, actAsTransactionTimeout)
	if err != nil {
		t.Logf("cleanup: open fdb: %v", err)
		return
	}
	end := append(append([]byte(nil), prefix...), 0xFF)
	_, err = db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	})
	if err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}

// writeActAsOrg writes a root org node with the store, the way the seed
// command writes one, and seeds the org's node types and property defs.
func writeActAsOrg(t *testing.T, stores *fdbadapter.Stores, slug string) uuid.UUID {
	t.Helper()
	orgID := node.NewOrgID()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{"slug": actAsJSONString(t, slug)}
	value := &node.Node{
		ID: orgID, OrgID: orgID, NodeType: "org", Name: slug, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: orgID, OrgID: orgID, NodeType: "org", Name: slug, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	if err := stores.Nodes.CreateAtomic(t.Context(), value, view, nil, []string{"slug"}, nil, nil); err != nil {
		t.Fatalf("write org %s: %v", slug, err)
	}
	if err := service.NewSeeder(stores.PropertyDefs, stores.NodeTypes).SeedOrg(t.Context(), orgID); err != nil {
		t.Fatalf("seed org %s: %v", slug, err)
	}
	return orgID
}

// createActAsScope creates a workspace or project through the node service.
// A project also gets an identifier for its issue references.
func createActAsScope(t *testing.T, nodes *service.NodeService, typeKey, name string, parentID uuid.UUID) uuid.UUID {
	t.Helper()
	props := map[string]json.RawMessage{"slug": actAsJSONString(t, strings.ToLower(name))}
	if typeKey == "project" {
		props["identifier"] = actAsJSONString(t, strings.ToUpper(name))
	}
	created, err := nodes.Create(t.Context(), service.CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: typeKey, Name: name, Props: props,
		Relationships: nil, ActorID: uuid.New(),
		IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		t.Fatalf("create %s %s: %v", typeKey, name, err)
	}
	return created.View.ID
}

func actAsJSONString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %q: %v", value, err)
	}
	return encoded
}

// actAsOutboxRows reads every row in the FoundationDB operator outbox under
// the test's key prefix. The store writes a staged user row there inside the
// create transaction.
func actAsOutboxRows(t *testing.T, stores *fdbadapter.Stores) []audit.Event {
	t.Helper()
	entries, err := stores.OpsOutbox.ReadOutboxFrom(context.WithoutCancel(t.Context()), nil, 100)
	if err != nil {
		t.Fatalf("read the foundationdb outbox: %v", err)
	}
	rows := make([]audit.Event, 0, len(entries))
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode the outbox row: %v", err)
		}
		rows = append(rows, event)
	}
	return rows
}

// actAsIssues lists the issue nodes in orgID through the node reader.
func actAsIssues(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID) []*node.NodeView {
	t.Helper()
	views, err := stores.Views.List(t.Context(), node.NodeListQuery{OrgID: orgID, NodeType: "issue"})
	if err != nil {
		t.Fatalf("list issues in org %s: %v", orgID, err)
	}
	return views
}
