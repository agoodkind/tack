package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateRejectsUndeclaredProp(t *testing.T) {
	orgID := uuid.New()
	parentID := uuid.New()
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{}}
	service := NewNodeService(
		repo,
		reader,
		&idempotencyTypes{types: []*node.NodeType{{TypeKey: "issue", Slug: "issue"}}},
		&idempotencyProps{defs: []*node.PropertyDef{{Name: "scope_id"}, {Name: "parent_id"}}},
		nil,
		nil,
		idempotencySearcher{},
	)

	_, err := service.Create(context.Background(), CreateInput{
		ParentID:    parentID,
		ScopeID:     parentID,
		NodeTypeKey: "issue",
		Name:        "Bad",
		Props:       map[string]json.RawMessage{"made_up_field": mustJSONRaw(t, "x")},
		ActorID:     uuid.New(),
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Create err = %v, want ErrInvalidArgument", err)
	}
}

func TestUpdateRejectsUndeclaredProp(t *testing.T) {
	orgID := uuid.New()
	nodeID := uuid.New()
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{
		nodeID: {
			ID:       nodeID,
			OrgID:    orgID,
			NodeType: "issue",
			Name:     "Existing",
			Props: map[string]json.RawMessage{
				"parent_id": mustJSONRaw(t, uuid.New().String()),
				"scope_id":  mustJSONRaw(t, uuid.New().String()),
			},
		},
	}}
	service := NewNodeService(
		repo,
		reader,
		&idempotencyTypes{types: []*node.NodeType{{TypeKey: "issue", Slug: "issue"}}},
		&idempotencyProps{defs: []*node.PropertyDef{{Name: "scope_id"}, {Name: "parent_id"}}},
		nil,
		nil,
		idempotencySearcher{},
	)

	_, err := service.Update(context.Background(), UpdateInput{
		NodeID:  nodeID,
		Props:   map[string]json.RawMessage{"made_up_field": mustJSONRaw(t, "x")},
		ActorID: uuid.New(),
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("Update err = %v, want ErrInvalidArgument", err)
	}
}

func mustJSONRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
