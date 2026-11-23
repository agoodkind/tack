package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// putSeedlessMetadata writes the required metadata with PropertyDefs.Set and
// NodeTypes.Set. Tack does not expose a public operation for creating
// property definitions or node types. ops qa datagen also uses these stores.
func putSeedlessMetadata(
	t *testing.T,
	env seedlessEnvironment,
	orgID uuid.UUID,
	keys seedlessKeys,
) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())

	definitions := newSeedlessDefinitions(orgID, keys)
	for _, definition := range definitions.all() {
		if err := env.Stores.PropertyDefs.Set(ctx, definition); err != nil {
			t.Fatalf("store property definition %s: %v", definition.Name, err)
		}
	}

	for _, kind := range newSeedlessTypes(orgID, keys, definitions) {
		if err := env.Stores.NodeTypes.Set(ctx, kind); err != nil {
			t.Fatalf("store node type %s: %v", kind.TypeKey, err)
		}
	}
}

func putSeedlessHierarchyNode(
	t *testing.T,
	env seedlessEnvironment,
	value *node.Node,
	parentID uuid.UUID,
	indexed []string,
) {
	t.Helper()
	relationships := make([]*node.Relationship, 0, 1)
	if parentID != uuid.Nil {
		value.Props[seedlessParentProperty] = json.RawMessage(strconv.Quote(parentID.String()))
		relationships = append(
			relationships,
			&node.Relationship{
				OrgID:        value.OrgID,
				SourceID:     value.ID,
				RelationType: node.RelChildOf,
				TargetID:     parentID,
				CreatedBy:    uuid.Nil,
				CreatedAt:    value.CreatedAt,
				Props:        nil,
			},
		)
	}

	view := node.NodeView(*value)
	if err := env.Stores.Nodes.CreateAtomic(
		integration.WithTestOperation(t, t.Context()),
		value,
		&view,
		relationships,
		indexed,
		nil,
		nil,
	); err != nil {
		t.Fatalf("create node %s of type %s: %v", value.ID, value.NodeType, err)
	}
}

func newSeedlessHierarchyNode(
	orgID uuid.UUID,
	id uuid.UUID,
	typeKey string,
	name string,
	addressKey string,
	address string,
) *node.Node {
	now := clock.Now().UTC()

	return &node.Node{
		ID:        id,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     map[string]json.RawMessage{addressKey: json.RawMessage(strconv.Quote(address))},
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
