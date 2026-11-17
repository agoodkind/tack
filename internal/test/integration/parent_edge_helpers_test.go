package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// parentEdgeDeadline bounds each wait for search work or a replacement.
	parentEdgeDeadline = 10 * time.Minute
	// parentEdgeSearchPages bounds the tack_search pages of one traversal.
	parentEdgeSearchPages = 1000
	// parentEdgeSummaryBytes bounds the length of each summary name.
	parentEdgeSummaryBytes = 256
)

func (f parentEdgeFixture) createLeaf(t *testing.T, parentID uuid.UUID, text string) uuid.UUID {
	t.Helper()
	created, err := f.nodes.Create(
		integration.WithTestOperation(t, t.Context()),
		service.CreateInput{
			ParentID:    parentID,
			NodeTypeKey: f.kind.typeKey,
			Name:        text,
			ActorID:     f.first.Actors[0].UserID,
			Props: map[string]json.RawMessage{
				f.kind.included: parentEdgeJSON(t, text),
				f.kind.excluded: parentEdgeJSON(t, "excluded"),
			},
		},
	)
	if err != nil || created == nil || created.View == nil {
		t.Fatalf("create leaf %q under %s: %v", text, parentID, err)
	}

	f.titles.Add(datagen.SearchItem{Title: text, NodeType: f.kind.typeKey}, created.View.ID)

	return created.View.ID
}

// moveLeaf calls NodeService.Update to set the parent_id and scope_id of
// nodeID to parentID.
func (f parentEdgeFixture) moveLeaf(t *testing.T, nodeID, parentID uuid.UUID) error {
	t.Helper()
	target := parentEdgeJSON(t, parentID.String())
	_, err := f.nodes.Update(
		integration.WithTestOperation(t, t.Context()),
		service.UpdateInput{
			NodeID:  nodeID,
			Props:   map[string]json.RawMessage{"parent_id": target},
			ScopeID: parentID,
			ActorID: f.first.Actors[0].UserID,
		},
	)

	return err
}

// storeNode creates one node of kind with relationships through the node
// store, which schedules the search work of the node.
func (f parentEdgeFixture) storeNode(
	t *testing.T,
	kind parentEdgeKind,
	text string,
	relationships ...*node.Relationship,
) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{
		kind.included: parentEdgeJSON(t, text),
		kind.excluded: parentEdgeJSON(t, "excluded"),
	}
	now := clock.Now().UTC()
	created := &node.Node{
		ID:        nodeID,
		OrgID:     kind.orgID,
		NodeType:  kind.typeKey,
		Name:      text,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        nodeID,
		OrgID:     kind.orgID,
		NodeType:  kind.typeKey,
		Name:      text,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, relationship := range relationships {
		relationship.OrgID, relationship.SourceID, relationship.CreatedAt = kind.orgID, nodeID, now
	}

	if err := f.stores.Nodes.CreateAtomic(
		integration.WithTestOperation(t, t.Context()),
		created,
		view,
		relationships,
		nil,
		nil,
		nil,
	); err != nil {
		t.Fatalf("store node %q: %v", text, err)
	}

	f.titles.Add(datagen.SearchItem{Title: text, NodeType: kind.typeKey}, nodeID)

	return nodeID
}
