package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestCreateAtomicRejectsDuplicateReference(t *testing.T) {
	env := SetupTestEnv(t)
	first := referenceTestNode(env.OrgID)
	firstView := referenceTestView(first)
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "taken"}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, firstView, nil, nil, []node.ReferenceKey{key}, nil); err != nil {
		t.Fatalf("create first node: %v", err)
	}
	second := referenceTestNode(env.OrgID)
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, second, referenceTestView(second), nil, nil, []node.ReferenceKey{key}, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create duplicate node error = %v, want ErrConflict", err)
	}
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, second.ID)
	if err != nil {
		t.Fatalf("get rejected node: %v", err)
	}
	if stored != nil {
		t.Fatalf("rejected node = %+v, want nil", stored)
	}
	missing, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "missing")
	if err != nil {
		t.Fatalf("lookup missing reference: %v", err)
	}
	if missing != uuid.Nil {
		t.Fatalf("missing reference node ID = %s, want nil UUID", missing)
	}
}

func referenceTestNode(orgID uuid.UUID) *node.Node {
	now := clock.Now().UTC()
	return &node.Node{ID: uuid.New(), OrgID: orgID, NodeType: "test", Name: "Test", CreatedAt: now, UpdatedAt: now}
}

func referenceTestView(current *node.Node) *node.NodeView {
	return &node.NodeView{ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt}
}
