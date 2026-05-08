package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateWritesReferenceAddress(t *testing.T) {
	orgID := uuid.New()
	parentID := uuid.New()
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{}}
	types := []*node.NodeType{{
		TypeKey:   "project",
		Slug:      "project",
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}, {Name: "identifier"}}
	service := NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{})

	result, err := service.Create(context.Background(), CreateInput{
		ParentID:    parentID,
		ScopeID:     parentID,
		NodeTypeKey: "project",
		Name:        "Tack",
		Props:       map[string]json.RawMessage{"identifier": mustJSONRaw(t, "TACK")},
		ActorID:     uuid.New(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(repo.addressWrites) != 1 {
		t.Fatalf("address writes = %d, want 1", len(repo.addressWrites))
	}
	write := repo.addressWrites[0]
	if write.NodeType != "project" || write.AddressKind != node.AddressKindPrimary || write.Address != "TACK" || write.NodeID != result.View.ID {
		t.Fatalf("address write = %+v, result id = %s", write, result.View.ID)
	}
}

func TestUpdateReconcilesReferenceAddress(t *testing.T) {
	orgID := uuid.New()
	nodeID := uuid.New()
	parentID := uuid.New()
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{
		nodeID: {
			ID:       nodeID,
			OrgID:    orgID,
			NodeType: "project",
			Name:     "Tack",
			Props: map[string]json.RawMessage{
				"parent_id":  mustJSONRaw(t, parentID.String()),
				"scope_id":   mustJSONRaw(t, parentID.String()),
				"identifier": mustJSONRaw(t, "OLD"),
			},
		},
	}}
	types := []*node.NodeType{{
		TypeKey:   "project",
		Slug:      "project",
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "identifier"},
	}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}, {Name: "identifier"}}
	service := NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{})

	_, err := service.Update(context.Background(), UpdateInput{
		NodeID:  nodeID,
		Props:   map[string]json.RawMessage{"identifier": mustJSONRaw(t, "NEW")},
		ActorID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(repo.addressDeletes) != 1 {
		t.Fatalf("address deletes = %d, want 1", len(repo.addressDeletes))
	}
	deleteAddress := repo.addressDeletes[0]
	if deleteAddress.NodeType != "project" || deleteAddress.AddressKind != node.AddressKindPrimary || deleteAddress.Address != "OLD" {
		t.Fatalf("address delete = %+v", deleteAddress)
	}
	if len(repo.addressWrites) != 1 {
		t.Fatalf("address writes = %d, want 1", len(repo.addressWrites))
	}
	write := repo.addressWrites[0]
	if write.NodeType != "project" || write.AddressKind != node.AddressKindPrimary || write.Address != "NEW" || write.NodeID != nodeID {
		t.Fatalf("address write = %+v, node id = %s", write, nodeID)
	}
}
