package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// clearReferenceTemplates removes templates from the specified types to test
// org-defined types without reference constraints.
func clearReferenceTemplates(t *testing.T, env *TestEnv, typeKeys ...string) {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}

	wanted := make(map[string]bool, len(typeKeys))
	for _, typeKey := range typeKeys {
		wanted[typeKey] = true
	}

	for _, nodeType := range types {
		if !wanted[nodeType.TypeKey] {
			continue
		}

		nodeType.ReferenceTemplates = nil
		if err := env.Stores.NodeTypes.Set(env.Ctx, nodeType); err != nil {
			t.Fatalf("clear templates for %s: %v", nodeType.TypeKey, err)
		}
	}
}

// writeNodeWithSplitScope writes different node IDs for parent_id and scope_id
// to create the inconsistency that scope repair corrects.
func writeNodeWithSplitScope(
	t *testing.T,
	env *TestEnv,
	typeKey string,
	name string,
	parentID uuid.UUID,
	scopeID uuid.UUID,
	sequence int,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": jsonStr(parentID.String()),
		"scope_id":  jsonStr(scopeID.String()),
		"sequence":  jsonNumber(sequence),
	}
	current := &node.Node{
		ID:        uuid.Must(uuid.NewV7()),
		OrgID:     env.OrgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        current.ID,
		OrgID:     current.OrgID,
		NodeType:  current.NodeType,
		Name:      current.Name,
		Props:     props,
		CreatedAt: current.CreatedAt,
		UpdatedAt: current.UpdatedAt,
	}
	if err := env.Stores.Nodes.CreateAtomic(
		env.Ctx,
		current,
		view,
		[]*node.Relationship{
			{
				OrgID:        env.OrgID,
				SourceID:     current.ID,
				RelationType: node.RelChildOf,
				TargetID:     parentID,
				CreatedAt:    now,
			},
		},
		[]string{"parent_id", "scope_id", "sequence"},
		nil,
		nil,
	); err != nil {
		t.Fatalf("write %s with split scope: %v", typeKey, err)
	}

	return view
}
