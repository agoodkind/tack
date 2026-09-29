package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// maxDeleteEdgePage bounds the edges of one node that one subtree delete
// step reads in each direction.
const maxDeleteEdgePage = maxRelationshipPage

// deleteEdge is one relationship of a node in a subtree delete.
type deleteEdge struct {
	SourceID     uuid.UUID
	RelationType string
	TargetID     uuid.UUID
}

// readDeleteEdges reads at most maxDeleteEdgePage+1 edges of relationType of
// nodeID inside tr. A result longer than maxDeleteEdgePage means more edges
// exist than one step handles. An empty relationType reads edges of every
// type. With incoming true it reads the edges to nodeID, and otherwise the
// edges from nodeID.
func readDeleteEdges(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID, relationType string, incoming bool) ([]deleteEdge, error) {
	limit := maxDeleteEdgePage + 1
	prefix := relationshipPrefixBySource(orgID, nodeID, relationType)
	if incoming {
		prefix = relationshipReversePrefixByTarget(orgID, nodeID, relationType)
	}
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create relationship range for node "+nodeID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read relationships of node "+nodeID.String(), err)
	}
	edges := make([]deleteEdge, 0, len(items))
	for _, item := range items {
		relationType, otherID, err := decodeDeleteEdge(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		if incoming {
			edges = append(edges, deleteEdge{SourceID: otherID, RelationType: relationType, TargetID: nodeID})
		} else {
			edges = append(edges, deleteEdge{SourceID: nodeID, RelationType: relationType, TargetID: otherID})
		}
	}
	return edges, nil
}

// decodeDeleteEdge returns the relation type and the other end of one
// forward or reverse relationship key.
func decodeDeleteEdge(ctx context.Context, key fdb.Key) (string, uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return "", uuid.Nil, searchReadFailure(ctx, "unpack relationship key", err)
	}
	if len(values) < 5 {
		return "", uuid.Nil, searchReadFailure(ctx, "decode relationship key", fmt.Errorf("relationship key has %d tuple elements", len(values)))
	}
	relationType, isText := values[3].(string)
	if !isText {
		return "", uuid.Nil, searchReadFailure(ctx, "decode relationship key", fmt.Errorf("relationship type is %T, not a string", values[3]))
	}
	otherID, err := edgeEnd(ctx, key)
	if err != nil {
		return "", uuid.Nil, err
	}
	return relationType, otherID, nil
}

// clearDeleteEdges clears the forward and reverse key of every edge inside
// tr. When search work is enabled, it schedules access work for both ends of
// every cleared edge, the same work a relationship removal schedules.
func (s *NodeDeleteStore) clearDeleteEdges(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, edges []deleteEdge) error {
	removed := make([]*node.Relationship, 0, len(edges))
	for _, edge := range edges {
		tr.Clear(fdb.Key(relationshipKey(orgID, edge.SourceID, edge.RelationType, edge.TargetID)))
		tr.Clear(fdb.Key(relationshipReverseKey(orgID, edge.TargetID, edge.RelationType, edge.SourceID)))
		removed = append(removed, &node.Relationship{
			OrgID: orgID, SourceID: edge.SourceID, RelationType: edge.RelationType, TargetID: edge.TargetID,
			CreatedBy: uuid.Nil, CreatedAt: time.Time{}, Props: map[string]json.RawMessage{},
		})
	}
	if !s.nodes.searchWork || len(removed) == 0 {
		return nil
	}
	return scheduleRelatedSearchWork(ctx, tr, s.nodes.clock.Now(), []node.RelationshipChanges{
		{Add: []*node.Relationship{}, Remove: removed},
	})
}
