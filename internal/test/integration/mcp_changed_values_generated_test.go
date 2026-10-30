package integration_test

import (
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// putGeneratedTextType stores a node type and text property definition in the
// harness organization through the production FoundationDB stores.
// It returns their UUID-derived keys.
func putGeneratedTextType(t *testing.T, harness *integration.MCPHarness) (typeKey, propertyKey string) {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), integration.StoreTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	ctx := integration.WithTestOperation(t, t.Context())
	orgID := harness.OrgID()
	typeKey, propertyKey = opaqueSeedlessKey("t"), opaqueSeedlessKey("p")
	definition := seedlessDefinition(orgID, propertyKey, node.PropertyTypeText, nil)
	if err := stores.PropertyDefs.Set(ctx, definition); err != nil {
		t.Fatalf("store property definition %s: %v", propertyKey, err)
	}
	nodeType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: typeKey, Slug: typeKey, Color: "", Icon: "",
		AllowedOps: node.AllOps, PropertyDefIDs: []uuid.UUID{definition.ID}, PluralSlug: typeKey + "s",
		IsBuiltin: false, TypeKey: typeKey, Features: nil, CanContain: nil,
		CanLiveUnder:       integration.EntryTypeKeys(t, stores, orgID),
		Reference:          node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly, Property: ""},
		ReferenceTemplates: nil, DefaultChildren: nil,
	}
	if err := stores.NodeTypes.Set(ctx, nodeType); err != nil {
		t.Fatalf("store node type %s: %v", typeKey, err)
	}
	return typeKey, propertyKey
}

// TestChangedGeneratedTypeValues checks create and update confirmations for a node
// type and text property with generated keys. It requires exact Set, Changed, and
// Unchanged lines and limits each confirmation to 1024 bytes.
func TestChangedGeneratedTypeValues(t *testing.T) {
	seeded := integration.NewMCPHarness(t)
	typeKey, propertyKey := putGeneratedTextType(t, seeded)
	harness := seeded.SecondProcess(t)
	createArguments := changedValueArguments(harness, "")
	createArguments.Name = "generated before rename"
	createArguments.Properties = datagen.NodeProperties{propertyKey: changedValueText("generated text before")}
	created := harness.Call(t, "tack_create_"+typeKey, createArguments)
	update := func() string {
		arguments := changedValueArguments(harness, "")
		arguments.NodeID = created.RawID()
		arguments.Name = "generated after rename"
		arguments.Properties = datagen.NodeProperties{propertyKey: changedValueText("generated text value")}
		return harness.Call(t, "tack_update_"+typeKey, arguments).Text()
	}

	first := update()
	repeated := update()

	requireConfirmationLines(t, created.Text(), "- Set: `"+propertyKey+"` to `generated text before`")
	requireConfirmationLines(t, first,
		"- Changed: `name` from `generated before rename` to `generated after rename`",
		"- Changed: `"+propertyKey+"` from `generated text before` to `generated text value`",
	)
	requireConfirmationLines(t, repeated,
		"- Unchanged: `name` is `generated after rename`",
		"- Unchanged: `"+propertyKey+"` is `generated text value`",
	)
	for _, text := range []string{created.Text(), first, repeated} {
		if len(text) > changedValuesMaxBytes {
			t.Fatalf("confirmation is %d bytes, want at most %d:\n%s", len(text), changedValuesMaxBytes, text)
		}
	}
}
