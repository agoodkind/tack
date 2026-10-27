package ops

import (
	"encoding/json"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func rawUUIDProp(props map[string]json.RawMessage, key string) uuid.UUID {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return uuid.Nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func rawUUIDView(view *node.NodeView, key string) uuid.UUID {
	if view == nil {
		return uuid.Nil
	}
	return rawUUIDProp(view.Props, key)
}

func stringProp(props map[string]json.RawMessage, key string) string {
	raw, ok := props[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func viewFromRepairedNode(currentNode *node.Node) *node.NodeView {
	return &node.NodeView{
		ID:        currentNode.ID,
		OrgID:     currentNode.OrgID,
		NodeType:  currentNode.NodeType,
		Name:      currentNode.Name,
		Props:     maps.Clone(currentNode.Props),
		CreatedBy: currentNode.CreatedBy,
		UpdatedBy: currentNode.UpdatedBy,
		CreatedAt: currentNode.CreatedAt,
		UpdatedAt: currentNode.UpdatedAt,
	}
}
