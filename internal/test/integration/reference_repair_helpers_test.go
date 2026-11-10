package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func mustCreateLegacyRepairReference(
	t *testing.T,
	env *TestEnv,
	typeKey, name string,
	projectID uuid.UUID,
	sequence int,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": jsonStr(projectID.String()),
		"scope_id":  jsonStr(projectID.String()),
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
		env.Ctx, current, view, nil,
		[]string{"parent_id", "scope_id", "sequence"}, nil, nil,
	); err != nil {
		t.Fatalf("create legacy %s: %v", typeKey, err)
	}
	return view
}

// writeNodeWithRawGeneratedValue writes a node whose generated property holds
// the given JSON verbatim, so a test can present a value the seeding phase
// cannot read as a number.
func writeNodeWithRawGeneratedValue(
	t *testing.T,
	env *TestEnv,
	typeKey, name string,
	projectID uuid.UUID,
	rawSequence string,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": jsonStr(projectID.String()),
		"scope_id":  jsonStr(projectID.String()),
		"sequence":  json.RawMessage(rawSequence),
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
		env.Ctx, current, view, nil,
		[]string{"parent_id", "scope_id", "sequence"}, nil, nil,
	); err != nil {
		t.Fatalf("write %s with raw generated value: %v", typeKey, err)
	}
	return view
}
