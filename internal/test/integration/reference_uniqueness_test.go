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

func TestAllocateSequenceByKeyReturnsConsecutiveValues(t *testing.T) {
	env := SetupTestEnv(t)
	for want := int64(1); want <= 3; want++ {
		got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
		if err != nil {
			t.Fatalf("AllocateSequenceByKey: %v", err)
		}
		if got != want {
			t.Fatalf("sequence = %d, want %d", got, want)
		}
	}
}

func TestAllocateSequenceByKeySeparatesKeys(t *testing.T) {
	env := SetupTestEnv(t)
	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-issue-")
	if err != nil {
		t.Fatalf("allocate issue sequence: %v", err)
	}
	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-epic-")
	if err != nil {
		t.Fatalf("allocate epic sequence: %v", err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("sequences = %d, %d, want independent first values", first, second)
	}
}

func TestSeedSequenceByKeyRaisesCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("SeedSequenceByKey: %v", err)
	}
	got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("AllocateSequenceByKey: %v", err)
	}
	if got != 41 {
		t.Fatalf("sequence = %d, want 41", got)
	}
}

func TestSeedSequenceByKeyNeverLowersCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("seed counter to 40: %v", err)
	}
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 10); err != nil {
		t.Fatalf("seed counter to 10: %v", err)
	}
	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if first != 41 || second != 42 {
		t.Fatalf("sequences = %d, %d, want 41, 42", first, second)
	}
}

func referenceTestNode(orgID uuid.UUID) *node.Node {
	now := clock.Now().UTC()
	return &node.Node{ID: uuid.New(), OrgID: orgID, NodeType: "test", Name: "Test", CreatedAt: now, UpdatedAt: now}
}

func referenceTestView(current *node.Node) *node.NodeView {
	return &node.NodeView{ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt}
}
