package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// shortLease is the lease of a claim that a test lets expire in real time.
const shortLease = 2 * time.Second

// waitPastLeases waits in real time until every claim in works has expired.
func waitPastLeases(t *testing.T, works ...searchdomain.Work) {
	t.Helper()
	var latest time.Time
	for _, work := range works {
		if work.LeaseUntil.After(latest) {
			latest = work.LeaseUntil
		}
	}
	waitUntil(t, latest)
}

// newGrandchild stores a node of a new type that lists the moved child's
// type in CanLiveUnder, with one parent edge to the moved child.
func newGrandchild(t *testing.T, stores *fdbadapter.Stores, moved movableChild, text string) searchFixture {
	t.Helper()
	grandchild := moved.Fixture
	grandchild.NodeID = uuid.Must(uuid.NewV7())
	grandchild.TypeKey = opaqueSearchKey("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: grandchild.OrgID, Name: grandchild.TypeKey, TypeKey: grandchild.TypeKey,
		Slug: grandchild.TypeKey, PluralSlug: grandchild.TypeKey + "s", CanLiveUnder: []string{moved.Fixture.TypeKey},
	}
	if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
		t.Fatalf("store grandchild node type: %v", err)
	}
	props := map[string]json.RawMessage{grandchild.IncludedKey: mustJSON(text), grandchild.ExcludedKey: mustJSON(readerExcludedValue)}
	now := clock.Now().UTC()
	value := &node.Node{ID: grandchild.NodeID, OrgID: grandchild.OrgID, NodeType: grandchild.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: grandchild.NodeID, OrgID: grandchild.OrgID, NodeType: grandchild.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: grandchild.OrgID, SourceID: grandchild.NodeID, RelationType: opaqueSearchKey("r"), TargetID: moved.Fixture.NodeID, CreatedBy: uuid.Nil, CreatedAt: now, Props: nil}
	if err := stores.Nodes.CreateAtomic(t.Context(), value, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("create grandchild node: %v", err)
	}
	return grandchild
}

// claimAccessFor claims access work until it claims the work of nodeID. It
// yields every other claim before it returns.
func claimAccessFor(t *testing.T, store *fdbadapter.SearchWorkStore, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	held := make([]searchdomain.Work, 0)
	defer func() {
		for _, work := range held {
			if err := store.Yield(t.Context(), work); err != nil {
				t.Errorf("yield inspected access work: %v", err)
			}
		}
	}()
	for range 100 {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassAccess, "access-inspector", time.Minute)
		if err != nil {
			t.Fatalf("claim access work of %s: %v", nodeID, err)
		}
		if work.NodeID == nodeID {
			return work
		}
		held = append(held, work)
	}
	t.Fatalf("access work of %s was not claimable", nodeID)
	return searchdomain.Work{}
}

// drainClass claims and processes work of one class until none is claimable.
func drainClass(t *testing.T, store *fdbadapter.SearchWorkStore, worker *service.SearchWorker, class searchdomain.WorkClass) {
	t.Helper()
	for range 1000 {
		work, err := store.Claim(t.Context(), class, "class-driver", time.Minute)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return
		}
		if err != nil {
			t.Fatalf("claim %s work: %v", class, err)
		}
		if err := worker.Process(t.Context(), work); err != nil {
			t.Fatalf("process %s work of %s: %v", class, work.NodeID, err)
		}
	}
	t.Fatalf("%s work did not drain within 1000 slices", class)
}

// runSearchWorkerWithin runs slices until no class has claimable work or
// the slice limit is spent.
func runSearchWorkerWithin(t *testing.T, worker *service.SearchWorker, limit int) {
	t.Helper()
	for range limit {
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("run search worker slice: %v", err)
		}
		if !claimed {
			return
		}
	}
	t.Fatalf("search work did not converge within %d slices", limit)
}
