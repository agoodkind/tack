package integration_test

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictEveryStoreWriterAdvancesTheFence calls the exported
// NodeStore writers CreateAtomic, UpdateAtomic, Set, and Delete with real
// FoundationDB. The test checks that each call increases the write fence
// bucket epoch of the written node. The seed command, the QA data generator,
// and ops repair and backfill commands call these writers.
func TestNodeIntegrityStrictEveryStoreWriterAdvancesTheFence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	record := storeWriterWorkspace(env, "store")
	written := func() []uuid.UUID { return []uuid.UUID{record.ID} }

	requireFenceAdvances(t, env, "NodeStore.CreateAtomic", func() []uuid.UUID {
		if err := env.Stores.Nodes.CreateAtomic(env.Ctx, record, storeWriterView(record), storeWriterParentEdge(record), []string{"slug"}, nil, nil); err != nil {
			t.Fatalf("create node %s: %v", record.ID, err)
		}
		return written()
	})
	requireFenceAdvances(t, env, "NodeStore.UpdateAtomic", func() []uuid.UUID {
		oldProps := record.Props
		record.Name, record.Props = "updated store writer workspace", maps.Clone(oldProps)
		record.Props["slug"] = quoted("store-updated")
		if err := env.Stores.Nodes.UpdateAtomic(env.Ctx, record, storeWriterView(record), oldProps, []string{"slug"}, nil); err != nil {
			t.Fatalf("update node %s: %v", record.ID, err)
		}
		return written()
	})
	requireFenceAdvances(t, env, "NodeStore.Set", func() []uuid.UUID {
		record.Name = "set store writer workspace"
		if err := env.Stores.Nodes.Set(env.Ctx, record, storeWriterView(record)); err != nil {
			t.Fatalf("set node %s: %v", record.ID, err)
		}
		return written()
	})
	requireFenceAdvances(t, env, "NodeStore.Delete", func() []uuid.UUID {
		if err := env.Stores.Nodes.Delete(env.Ctx, record.OrgID, record.ID); err != nil {
			t.Fatalf("delete node %s: %v", record.ID, err)
		}
		return written()
	})
}

// TestNodeIntegrityStrictRejectsInvalidStoreWriteAfterActivation activates
// strict validation, then calls NodeStore.CreateAtomic, UpdateAtomic, and Set
// with a record that has an undeclared property. It checks that each call fails
// with [domain.ErrInvalidArgument] and that the stored workspace stays
// unchanged.
func TestNodeIntegrityStrictRejectsInvalidStoreWriteAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	record := storeWriterWorkspace(env, "strict-store")
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, record, storeWriterView(record), storeWriterParentEdge(record), []string{"slug"}, nil, nil); err != nil {
		t.Fatalf("create node %s: %v", record.ID, err)
	}
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated {
		t.Fatalf("strict validation = %+v, %v; want activation", output.Result, err)
	}
	before := storedStoreView(t, env.Stores, record.ID)

	invalidProps := maps.Clone(record.Props)
	invalidProps["strict_test_undeclared"] = json.RawMessage(`"value"`)
	invalid := *record
	invalid.Props = invalidProps
	created := storeWriterWorkspace(env, "strict-store-created")
	created.Props = invalidProps
	writes := map[string]error{
		"NodeStore.CreateAtomic": env.Stores.Nodes.CreateAtomic(env.Ctx, created, storeWriterView(created), storeWriterParentEdge(created), []string{"slug"}, nil, nil),
		"NodeStore.UpdateAtomic": env.Stores.Nodes.UpdateAtomic(env.Ctx, &invalid, storeWriterView(&invalid), record.Props, []string{"slug"}, nil),
		"NodeStore.Set":          env.Stores.Nodes.Set(env.Ctx, &invalid, storeWriterView(&invalid)),
	}
	for writer, writeErr := range writes {
		if !errors.Is(writeErr, domain.ErrInvalidArgument) {
			t.Fatalf("%s with an undeclared property after activation = %v, want an invalid argument", writer, writeErr)
		}
	}
	after := storedStoreView(t, env.Stores, record.ID)
	if !reflect.DeepEqual(after.Props, before.Props) || after.Name != before.Name {
		t.Fatalf("the rejected writes changed workspace %s: before %+v, after %+v", record.ID, before, after)
	}
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, created.ID)
	if err != nil || stored != nil {
		t.Fatalf("node %s after the rejected create = %+v, %v; want no stored node", created.ID, stored, err)
	}
}

func storeWriterWorkspace(env *integration.TestEnv, slug string) *node.Node {
	now := clock.Now().UTC()
	return &node.Node{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: "workspace", Name: slug + " workspace",
		Props:     map[string]json.RawMessage{"slug": quoted(slug), "parent_id": quoted(env.OrgID.String())},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
}

func storeWriterParentEdge(record *node.Node) []*node.Relationship {
	return []*node.Relationship{{
		OrgID: record.OrgID, SourceID: record.ID, RelationType: node.RelChildOf, TargetID: record.OrgID,
		CreatedBy: uuid.Nil, CreatedAt: record.CreatedAt, Props: nil,
	}}
}

func storeWriterView(record *node.Node) *node.NodeView {
	return &node.NodeView{
		ID: record.ID, OrgID: record.OrgID, NodeType: record.NodeType, Name: record.Name, Props: record.Props,
		CreatedBy: record.CreatedBy, UpdatedBy: record.UpdatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}
