package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

const accessPageBytes = 64

// movableChild is one child node under the first of two entry points.
type movableChild struct {
	Fixture searchFixture
	First   uuid.UUID
	Second  uuid.UUID
}

// newMovableChild stores two entry-point nodes and one child of a type
// without the entry-point feature under the first entry point.
func newMovableChild(t *testing.T, stores *fdbadapter.Stores, text string) movableChild {
	t.Helper()
	first := putSearchText(t, stores, "first entry", readerExcludedValue)
	second := first
	second.NodeID = uuid.Must(uuid.NewV7())
	writeSearchNode(t, stores, second, "second entry", readerExcludedValue)
	child := first
	child.NodeID = uuid.Must(uuid.NewV7())
	child.TypeKey = opaqueSearchKey("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: first.OrgID, Name: child.TypeKey, TypeKey: child.TypeKey,
		Slug: child.TypeKey, PluralSlug: child.TypeKey + "s", CanLiveUnder: []string{first.TypeKey},
	}
	if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
		t.Fatalf("store child node type: %v", err)
	}
	props := map[string]json.RawMessage{child.IncludedKey: mustJSON(text), child.ExcludedKey: mustJSON(readerExcludedValue)}
	now := clock.Now().UTC()
	value := &node.Node{ID: child.NodeID, OrgID: child.OrgID, NodeType: child.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: child.NodeID, OrgID: child.OrgID, NodeType: child.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: child.OrgID, SourceID: child.NodeID, RelationType: node.RelChildOf, TargetID: first.NodeID, CreatedBy: uuid.Nil, CreatedAt: now, Props: nil}
	if err := stores.Nodes.CreateAtomic(t.Context(), value, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("create child node: %v", err)
	}
	return movableChild{Fixture: child, First: first.NodeID, Second: second.NodeID}
}

// moveChild removes the child's parent edge and adds one to the second entry point.
func moveChild(t *testing.T, stores *fdbadapter.Stores, moved movableChild) {
	t.Helper()
	orgID := moved.Fixture.OrgID
	if err := stores.Relationships.Remove(t.Context(), orgID, moved.Fixture.NodeID, node.RelChildOf, moved.First); err != nil {
		t.Fatalf("remove parent edge: %v", err)
	}
	parent := &node.Relationship{OrgID: orgID, SourceID: moved.Fixture.NodeID, RelationType: node.RelChildOf, TargetID: moved.Second, CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil}
	if err := stores.Relationships.Add(t.Context(), parent); err != nil {
		t.Fatalf("add parent edge: %v", err)
	}
}

// TestSearchAccessOnlyUpdateWithoutModel moves a child to another entry
// point with the model undeployed. Every current page must receive new
// access keys. The text, chunks, and sparse weights of each page must remain
// byte-for-byte unchanged.
func TestSearchAccessOnlyUpdateWithoutModel(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, model, index := newSearchIndex(t, stores)
	moved := newMovableChild(t, stores, strings.Repeat("semantic access page text ", 12))
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(accessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	before := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(before) < 2 {
		t.Fatalf("child has %d pages, want several", len(before))
	}
	undeployNativeModel(t, client, model.ID, func(ctx context.Context) error {
		_, err := adapter.Provision(ctx)
		return err
	})
	moveChild(t, stores, moved)
	runSearchWorkerUntilIdle(t, worker)
	after := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(after) != len(before) {
		t.Fatalf("access update changed the page count from %d to %d", len(before), len(after))
	}
	for position, page := range after {
		previous := before[position]
		if page.ID != previous.ID || *page.PageText != *previous.PageText || !bytes.Equal(page.Semantic, previous.Semantic) {
			t.Fatalf("page %d changed its document ID, text, chunks, or sparse weights", position)
		}
		if slices.Equal(page.Access.Keys, previous.Access.Keys) || pageGeneration(t, page) <= pageGeneration(t, previous) {
			t.Fatalf("page %d kept access %v at generation %s", position, page.Access.Keys, page.SearchGeneration)
		}
	}
}
