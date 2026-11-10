package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestUpdateAtomicRejectsDuplicateReferenceAndRollsBack(t *testing.T) {
	env := SetupTestEnv(t)
	first := referenceTestNode(env.OrgID)
	second := referenceTestNode(env.OrgID)
	second.Props = map[string]json.RawMessage{"status": json.RawMessage(`"before"`)}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, referenceTestView(first), nil, nil, nil, nil); err != nil {
		t.Fatalf("create first node: %v", err)
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, second, referenceTestView(second), nil, nil, nil, nil); err != nil {
		t.Fatalf("create second node: %v", err)
	}
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-42"}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, first.ID, []node.ReferenceKey{key}); err != nil {
		t.Fatalf("set first reference: %v", err)
	}
	updated := *second
	updated.Props = map[string]json.RawMessage{"status": json.RawMessage(`"after"`)}
	err := env.Stores.Nodes.UpdateAtomic(
		env.Ctx,
		&updated,
		referenceTestView(&updated),
		second.Props,
		nil,
		[]node.ReferenceKey{key},
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("update duplicate reference error = %v, want ErrConflict", err)
	}
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, second.ID)
	if err != nil {
		t.Fatalf("get rejected update: %v", err)
	}
	if string(stored.Props["status"]) != `"before"` {
		t.Fatalf("stored status = %s, want before", stored.Props["status"])
	}
}

func TestUpdateAtomicMovesReferenceOwnership(t *testing.T) {
	env := SetupTestEnv(t)
	first := referenceTestNode(env.OrgID)
	second := referenceTestNode(env.OrgID)
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, referenceTestView(first), nil, nil, nil, nil); err != nil {
		t.Fatalf("create first node: %v", err)
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, second, referenceTestView(second), nil, nil, nil, nil); err != nil {
		t.Fatalf("create second node: %v", err)
	}
	firstKey := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-1"}
	secondKey := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-2"}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, first.ID, []node.ReferenceKey{firstKey}); err != nil {
		t.Fatalf("set first reference: %v", err)
	}
	if err := env.Stores.Nodes.UpdateAtomic(env.Ctx, first, referenceTestView(first), first.Props, nil, []node.ReferenceKey{secondKey}); err != nil {
		t.Fatalf("move first reference: %v", err)
	}
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, second.ID, []node.ReferenceKey{firstKey}); err != nil {
		t.Fatalf("claim released reference: %v", err)
	}
	assertReferenceOwner(t, env, firstKey, second.ID)
	assertReferenceOwner(t, env, secondKey, first.ID)
}

// TestUpdateAtomicNilKeysPreserveOwnership pins the nil contract: an update
// that does not touch reference ownership must leave the node's existing
// claims in place. Without it, every ordinary update would release the node's
// reference and let another node take it.
func TestUpdateAtomicNilKeysPreserveOwnership(t *testing.T) {
	env := SetupTestEnv(t)
	current := referenceTestNode(env.OrgID)
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-9"}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, current, referenceTestView(current), nil, nil, []node.ReferenceKey{key}, nil); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := env.Stores.Nodes.UpdateAtomic(env.Ctx, current, referenceTestView(current), current.Props, nil, nil); err != nil {
		t.Fatalf("update with nil reference keys: %v", err)
	}
	assertReferenceOwner(t, env, key, current.ID)

	// A non-nil empty slice is the explicit release.
	if err := env.Stores.Nodes.UpdateAtomic(env.Ctx, current, referenceTestView(current), current.Props, nil, []node.ReferenceKey{}); err != nil {
		t.Fatalf("update with empty reference keys: %v", err)
	}
	assertReferenceOwner(t, env, key, uuid.Nil)
}

func TestDeleteReleasesReference(t *testing.T) {
	env := SetupTestEnv(t)
	current := referenceTestNode(env.OrgID)
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-55"}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, current, referenceTestView(current), nil, nil, []node.ReferenceKey{key}, nil); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := env.Stores.Nodes.Delete(env.Ctx, env.OrgID, current.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	assertReferenceOwner(t, env, key, uuid.Nil)
}

func assertReferenceOwner(t *testing.T, env *TestEnv, key node.ReferenceKey, want uuid.UUID) {
	t.Helper()
	got, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, key.TemplateName, key.Encoded)
	if err != nil {
		t.Fatalf("lookup reference %q: %v", key.Encoded, err)
	}
	if got != want {
		t.Fatalf("reference %q owner = %s, want %s", key.Encoded, got, want)
	}
}
