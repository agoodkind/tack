package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// decodeOrphanCandidate decodes one resolution record and reads its node
// type. It reports whether the type lists CanLiveUnder types.
func decodeOrphanCandidate(
	ctx context.Context,
	transaction fdb.Transaction,
	item fdb.KeyValue,
	kindsByOrg map[uuid.UUID]map[string]*node.NodeType,
) (orphanCandidate, bool, error) {
	none := orphanCandidate{
		node:     OrphanNode{ID: uuid.Nil, OrgID: uuid.Nil, NodeType: ""},
		parentID: uuid.Nil,
	}
	nodeID, err := lastTupleID(ctx, item.Key)
	if err != nil {
		return none, false, err
	}

	var resolved node.NodeResolve
	if err := json.Unmarshal(item.Value, &resolved); err != nil {
		return none, false, nodeOperationFailure(
			ctx,
			"decode node resolution "+nodeID.String(),
			err,
		)
	}

	if kindsByOrg[resolved.OrgID] == nil {
		kindsByOrg[resolved.OrgID] = make(map[string]*node.NodeType)
	}

	kind, err := cachedNodeTypeByKey(
		ctx,
		transaction,
		resolved.OrgID,
		resolved.NodeType,
		kindsByOrg[resolved.OrgID],
	)
	if err != nil || kind == nil || len(kind.CanLiveUnder) == 0 {
		return none, false, err
	}

	return orphanCandidate{
		node: OrphanNode{
			ID:       nodeID,
			OrgID:    resolved.OrgID,
			NodeType: resolved.NodeType,
		},
		parentID: uuid.Nil,
	}, true, nil
}
