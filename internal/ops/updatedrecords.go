package ops

import (
	"encoding/json"
	"maps"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func repairedScopeRecords(
	currentNode *node.Node,
	scopeID uuid.UUID,
) (*node.Node, *node.NodeView, map[string]json.RawMessage) {
	oldProps := maps.Clone(currentNode.Props)
	newProps := maps.Clone(currentNode.Props)
	newProps["scope_id"] = json.RawMessage(strconv.Quote(scopeID.String()))

	updatedNode := &node.Node{
		ID:        currentNode.ID,
		OrgID:     currentNode.OrgID,
		NodeType:  currentNode.NodeType,
		Name:      currentNode.Name,
		Props:     newProps,
		CreatedBy: currentNode.CreatedBy,
		UpdatedBy: currentNode.UpdatedBy,
		CreatedAt: currentNode.CreatedAt,
		UpdatedAt: currentNode.UpdatedAt,
	}
	updatedView := viewFromRepairedNode(updatedNode)

	return updatedNode, updatedView, oldProps
}
