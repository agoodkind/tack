package ops

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func repairScopeID(view *node.NodeView, fields []string) uuid.UUID {
	for _, field := range fields {
		if nodeID := rawUUIDProp(view.Props, field); nodeID != uuid.Nil {
			return nodeID
		}
	}

	return uuid.Nil
}
