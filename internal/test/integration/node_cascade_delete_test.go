package integration

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

// TestDeleteProjectDeletesDescendants deletes a project through the node
// service, the path tack_delete_project calls, with the tool wrapper's audit
// slot attached. Every state, epic, issue, and comment under the project must
// be gone, and no remaining node may keep a parent_id that refers to a
// deleted node. The delete must report the count of deleted nodes, write one
// node.delete ledger event per deleted node, and schedule search cleanup work
// for every deleted node.
func TestDeleteProjectDeletesDescendants(t *testing.T) {
	env := SetupTestEnv(t)
	env.Stores.EnableSearchWork()
	fixture := newCascadeFixture(t, env)
	drainOutbox(t, env)
	actor := uuid.New()
	ctx := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_delete_project", actor)

	result, err := env.NodeSvc.Delete(ctx, fixture.Project, actor)
	if err != nil {
		t.Fatalf("delete project %s: %v", fixture.Project, err)
	}

	removed := append([]uuid.UUID{fixture.Project}, fixture.Descendants...)
	if result.Deleted != len(removed) || result.State != node.SubtreeDeleteFinished {
		t.Fatalf("delete result = %+v, want %d deleted nodes and a finished job", result, len(removed))
	}
	if !auditintent.Committed(ctx) {
		t.Fatal("the store must report the staged row of the project committed with its delete")
	}
	requireNodesGone(t, env, removed)
	requireNodesPresent(t, env, fixture.Kept)
	requireNoDanglingParents(t, env)
	requireDeleteEvents(t, env, removed, actor)
	cleanup := storedSearchWork(t, searchdomain.WorkClassCleanup)
	for _, nodeID := range removed {
		if _, scheduled := cleanup[nodeID]; !scheduled {
			t.Errorf("node %s has no search cleanup work after its delete", nodeID)
		}
	}
}

// storedSearchWork reads the node IDs of every pending search work record of
// class under the active test prefix. A record key is (search_work, class,
// bucket, orgID, nodeID).
func storedSearchWork(t *testing.T, class searchdomain.WorkClass) map[uuid.UUID]struct{} {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to read search work: %v", err)
	}
	prefix := append([]byte{}, fdbadapter.TestPrefixRange()...)
	keyRange, err := fdb.PrefixRange(append(prefix, tuple.Tuple{"search_work", string(class)}.Pack()...))
	if err != nil {
		t.Fatalf("create search_work range: %v", err)
	}
	items, err := database.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("read search_work range: %v", err)
	}
	nodeIDs := map[uuid.UUID]struct{}{}
	for _, item := range items.([]fdb.KeyValue) {
		values, err := tuple.Unpack(item.Key[len(prefix):])
		if err != nil || len(values) != 5 {
			t.Fatalf("decode search_work key %x: %v", item.Key, err)
		}
		nodeText, _ := values[4].(string)
		nodeIDs[uuid.MustParse(nodeText)] = struct{}{}
	}
	return nodeIDs
}

// drainOutbox clears every outbox row under the test prefix.
func drainOutbox(t *testing.T, env *TestEnv) {
	t.Helper()
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, nil, 100)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return
		}
		if err := env.Stores.OpsOutbox.ClearThrough(env.Ctx, entries[len(entries)-1].Mark); err != nil {
			t.Fatalf("clear the outbox: %v", err)
		}
	}
}

// requireDeleteEvents requires exactly one node.delete outbox row by actor
// for each node in removed, and no other row.
func requireDeleteEvents(t *testing.T, env *TestEnv, removed []uuid.UUID, actor uuid.UUID) {
	t.Helper()
	events := outboxEvents(t, env)
	recorded := make([]uuid.UUID, 0, len(events))
	eventIDs := make(map[uuid.UUID]struct{}, len(events))
	for _, event := range events {
		if event.Verb != string(audit.VerbNodeDelete) || event.Actor.ID != actor || event.Context.Tool != "tack_delete_project" {
			t.Fatalf("outbox row = %+v, want node.delete by %s through tack_delete_project", event, actor)
		}
		eventIDs[event.EventID] = struct{}{}
		recorded = append(recorded, event.Entity.ID)
	}
	if len(eventIDs) != len(events) {
		t.Fatalf("outbox rows share event IDs: %d rows, %d distinct IDs", len(events), len(eventIDs))
	}
	slices.SortFunc(recorded, compareUUID)
	want := slices.Clone(removed)
	slices.SortFunc(want, compareUUID)
	if !slices.Equal(recorded, want) {
		t.Fatalf("outbox rows describe nodes %v, want %v", recorded, want)
	}
}

// outboxEvents reads and decodes every outbox row under the test prefix, in
// commit order.
func outboxEvents(t *testing.T, env *TestEnv) []audit.Event {
	t.Helper()
	events := []audit.Event{}
	var mark []byte
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, mark, 500)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return events
		}
		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil {
				t.Fatalf("decode an outbox row: %v", err)
			}
			events = append(events, event)
		}
		mark = entries[len(entries)-1].Mark
	}
}

func compareUUID(left, right uuid.UUID) int {
	return slices.Compare(left[:], right[:])
}
