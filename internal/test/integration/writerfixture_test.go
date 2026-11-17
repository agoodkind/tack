package integration_test

import (
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

func storeWriterWorkspace(env *integration.TestEnv, slug string) *node.Node {
	now := clock.Now().UTC()

	return &node.Node{
		ID:       uuid.Must(uuid.NewV7()),
		OrgID:    env.OrgID,
		NodeType: "workspace",
		Name:     slug + " workspace",
		Props: map[string]json.RawMessage{
			"slug": quoted(slug),
		},
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func storeWriterParentEdge(record *node.Node) []*node.Relationship {
	return []*node.Relationship{
		{
			OrgID:        record.OrgID,
			SourceID:     record.ID,
			RelationType: node.RelChildOf,
			TargetID:     record.OrgID,
			CreatedBy:    uuid.Nil,
			CreatedAt:    record.CreatedAt,
			Props:        nil,
		},
	}
}

func storeWriterView(record *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        record.ID,
		OrgID:     record.OrgID,
		NodeType:  record.NodeType,
		Name:      record.Name,
		Props:     record.Props,
		CreatedBy: record.CreatedBy,
		UpdatedBy: record.UpdatedBy,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}
