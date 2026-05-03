package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateIdempotencyRetryReturnsExistingNode(t *testing.T) {
	orgID := uuid.New()
	parentID := uuid.New()
	existingID := uuid.New()
	existingView := &node.NodeView{ID: existingID, OrgID: orgID, NodeType: "issue", Name: "Existing"}
	types := []*node.NodeType{{TypeKey: "issue", Slug: "issue"}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}}
	repo := &idempotencyNodeRepo{records: map[string]*node.IdempotencyRecord{
		"k": {Key: "k", NodeID: existingID, Fingerprint: "same", Source: "mcp"},
	}}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{existingID: existingView}}
	service := NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{})

	result, err := service.Create(context.Background(), CreateInput{
		ParentID:               parentID,
		ScopeID:                parentID,
		NodeTypeKey:            "issue",
		Name:                   "Existing",
		ActorID:                uuid.New(),
		IdempotencyKey:         "k",
		IdempotencyFingerprint: "same",
		IdempotencySource:      "mcp",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Existed || result.View.ID != existingID {
		t.Fatalf("result = %+v, want existing %s", result, existingID)
	}
}

func TestCreateIdempotencyConflictRejectsDifferentPayload(t *testing.T) {
	orgID := uuid.New()
	parentID := uuid.New()
	existingID := uuid.New()
	types := []*node.NodeType{{TypeKey: "issue", Slug: "issue"}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}}
	repo := &idempotencyNodeRepo{records: map[string]*node.IdempotencyRecord{
		"k": {Key: "k", NodeID: existingID, Fingerprint: "old", Source: "mcp"},
	}}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{}}
	service := NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{})

	_, err := service.Create(context.Background(), CreateInput{
		ParentID:               parentID,
		ScopeID:                parentID,
		NodeTypeKey:            "issue",
		Name:                   "Different",
		ActorID:                uuid.New(),
		IdempotencyKey:         "k",
		IdempotencyFingerprint: "new",
		IdempotencySource:      "mcp",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Create err = %v, want ErrConflict", err)
	}
}

func TestCreateStampsIdempotencyRecordAtomically(t *testing.T) {
	orgID := uuid.New()
	parentID := uuid.New()
	types := []*node.NodeType{{TypeKey: "issue", Slug: "issue"}}
	defs := []*node.PropertyDef{{Name: "parent_id"}, {Name: "scope_id"}}
	repo := &idempotencyNodeRepo{records: make(map[string]*node.IdempotencyRecord)}
	reader := &idempotencyReader{orgID: orgID, views: map[uuid.UUID]*node.NodeView{}}
	service := NewNodeService(repo, reader, &idempotencyTypes{types: types}, &idempotencyProps{defs: defs}, nil, nil, idempotencySearcher{})

	result, err := service.Create(context.Background(), CreateInput{
		ParentID:               parentID,
		ScopeID:                parentID,
		NodeTypeKey:            "issue",
		Name:                   "New",
		ActorID:                uuid.New(),
		IdempotencyKey:         "k",
		IdempotencyFingerprint: "fp",
		IdempotencySource:      "mcp",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.Existed {
		t.Fatal("first create should not be an existing result")
	}
	record := repo.records["k"]
	if record == nil || record.NodeID != result.View.ID || record.Fingerprint != "fp" || record.Source != "mcp" {
		t.Fatalf("idempotency record = %+v, result id = %s", record, result.View.ID)
	}
}
