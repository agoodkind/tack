package foundationdb_test

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func relationPageCandidate(
	id uuid.UUID,
	recordOrg uuid.UUID,
	parentID uuid.UUID,
	kind string,
	props map[string]json.RawMessage,
	now time.Time,
) (node.Node, node.NodeView) {
	current := node.Node{
		ID:        id,
		OrgID:     recordOrg,
		NodeType:  kind,
		Name:      "candidate",
		Props:     props,
		CreatedBy: parentID,
		UpdatedBy: parentID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := node.NodeView{
		ID:        id,
		OrgID:     recordOrg,
		NodeType:  kind,
		Name:      current.Name,
		Props:     props,
		CreatedBy: parentID,
		UpdatedBy: parentID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return current, view
}
