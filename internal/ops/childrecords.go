package ops

import (
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// hasNamed checks for a candidate with exactly the given Name. The seed
// supplies the name with the required case and spacing.
func hasNamed(candidates []*node.NodeView, name string) bool {
	for _, current := range candidates {
		if current.Name == name {
			return true
		}
	}

	return false
}

// defaultChildRecords returns a node, view, and child_of relationship under
// parentID with a new UUIDv7 ID and the current time. defaultChildProps adds
// parent_id and scope_id. It does not write. The ops package builds the records
// because importing the service package would create an import cycle.
func defaultChildRecords(
	orgID uuid.UUID,
	parentID uuid.UUID,
	defaultChild node.DefaultChild,
) (*node.Node, *node.NodeView, []*node.Relationship) {
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()

	parentRaw := json.RawMessage(strconv.Quote(parentID.String()))
	props := defaultChildProps(defaultChild, parentRaw)

	current := &node.Node{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  defaultChild.TypeKey,
		Name:      defaultChild.Name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
	}
	view := &node.NodeView{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  defaultChild.TypeKey,
		Name:      defaultChild.Name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
	}
	rels := []*node.Relationship{
		{
			OrgID:        orgID,
			SourceID:     nodeID,
			RelationType: node.RelChildOf,
			TargetID:     parentID,
			CreatedAt:    now,
			CreatedBy:    uuid.Nil,
			Props:        nil,
		},
	}

	return current, view, rels
}
