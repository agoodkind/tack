package integration_test

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"testing"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictRejectsInvalidStoreWriteAfterActivation activates
// strict validation, then calls NodeStore.CreateAtomic, UpdateAtomic, and Set
// with a record that has an undeclared property. It checks that each call fails
// with [domain.ErrInvalidArgument] and that the stored workspace stays
// unchanged.
func TestNodeIntegrityStrictRejectsInvalidStoreWriteAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	record := storeWriterWorkspace(env, "strict-store")
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
		"NodeStore.CreateAtomic": env.Stores.Nodes.CreateAtomic(
			env.Ctx,
			created,
			storeWriterView(created),
			storeWriterParentEdge(created),
			[]string{"slug"},
			nil,
			nil,
		),
		"NodeStore.UpdateAtomic": env.Stores.Nodes.UpdateAtomic(
			env.Ctx,
			&invalid,
			storeWriterView(&invalid),
			record.Props,
			[]string{"slug"},
			nil,
		),
		"NodeStore.Set": env.Stores.Nodes.Set(
			env.Ctx,
			&invalid,
			storeWriterView(&invalid),
		),
	}
	for writer, writeErr := range writes {
		if !errors.Is(writeErr, domain.ErrInvalidArgument) {
			t.Fatalf(
				"%s with an undeclared property after activation = %v, want an invalid argument",
				writer,
				writeErr,
			)
		}
	}

	after := storedStoreView(t, env.Stores, record.ID)
	if !reflect.DeepEqual(after.Props, before.Props) || after.Name != before.Name {
		t.Fatalf(
			"the rejected writes changed workspace %s: before %+v, after %+v",
			record.ID,
			before,
			after,
		)
	}

	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, created.ID)
	if err != nil || stored != nil {
		t.Fatalf(
			"node %s after the rejected create = %+v, %v; want no stored node",
			created.ID,
			stored,
			err,
		)
	}
}
