package integration_test

import (
	"maps"
	"testing"

	"github.com/google/uuid"
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

	requireFenceAdvances(
		t,
		env,
		"NodeStore.CreateAtomic",
		func() []uuid.UUID {
			if err := env.Stores.Nodes.CreateAtomic(
				env.Ctx,
				record,
				storeWriterView(record),
				storeWriterParentEdge(record),
				[]string{"slug"},
				nil,
				nil,
			); err != nil {
				t.Fatalf("create node %s: %v", record.ID, err)
			}

			return written()
		},
	)
	requireFenceAdvances(
		t,
		env,
		"NodeStore.UpdateAtomic",
		func() []uuid.UUID {
			oldProps := record.Props
			record.Name, record.Props = "updated store writer workspace", maps.Clone(oldProps)
			record.Props["slug"] = quoted("store-updated")
			if err := env.Stores.Nodes.UpdateAtomic(
				env.Ctx,
				record,
				storeWriterView(record),
				oldProps,
				[]string{"slug"},
				nil,
			); err != nil {
				t.Fatalf("update node %s: %v", record.ID, err)
			}

			return written()
		},
	)
	requireFenceAdvances(
		t,
		env,
		"NodeStore.Set",
		func() []uuid.UUID {
			record.Name = "set store writer workspace"
			if err := env.Stores.Nodes.Set(env.Ctx, record, storeWriterView(record)); err != nil {
				t.Fatalf("set node %s: %v", record.ID, err)
			}

			return written()
		},
	)
	requireFenceAdvances(
		t,
		env,
		"NodeStore.Delete",
		func() []uuid.UUID {
			if err := env.Stores.Nodes.Delete(env.Ctx, record.OrgID, record.ID); err != nil {
				t.Fatalf("delete node %s: %v", record.ID, err)
			}

			return written()
		},
	)
}
