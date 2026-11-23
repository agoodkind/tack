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
	typeKey string,
	name string,
	projectID uuid.UUID,
	sequence int,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"scope_id": jsonStr(projectID.String()),
		"sequence": jsonNumber(sequence),
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
				TargetID:     projectID,
				CreatedAt:    now,
			},
		},
		[]string{"scope_id", "sequence"},
		nil,
		nil,
	); err != nil {
		t.Fatalf("create legacy %s: %v", typeKey, err)
	}

	return view
}

// writeNodeWithRawGeneratedValue writes the given JSON verbatim to the
// generated property, including values that counter seeding cannot parse.
func writeNodeWithRawGeneratedValue(
	t *testing.T,
	env *TestEnv,
	typeKey string,
	name string,
	projectID uuid.UUID,
	rawSequence string,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"scope_id": jsonStr(projectID.String()),
		"sequence": json.RawMessage(rawSequence),
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
				TargetID:     projectID,
				CreatedAt:    now,
			},
		},
		[]string{"scope_id", "sequence"},
		nil,
		nil,
	); err != nil {
		t.Fatalf("write %s with raw generated value: %v", typeKey, err)
	}

	return view
}
