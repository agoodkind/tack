package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// nodesWithNames reports which of nodeIDs have a property value under one of
// the scan's names. It reads nothing when the scan requests no content or
// selects every node. It first issues every resolution read. It then waits
// for each resolution read in order and issues the node read for that
// resolution. It waits for the node reads only after it issues all of them.
func nodesWithNames(ctx context.Context, tr fdb.Transaction, nodeIDs []uuid.UUID, state searchScanRecord) (map[uuid.UUID]bool, error) {
	affected := make(map[uuid.UUID]bool, len(nodeIDs))
	if !state.Content || len(state.Names) == 0 {
		return affected, nil
	}
	resolves := make([]fdb.FutureByteSlice, len(nodeIDs))
	for position, nodeID := range nodeIDs {
		resolves[position] = tr.Get(fdb.Key(nodeResolveKey(nodeID)))
	}
	values := make([]fdb.FutureByteSlice, len(nodeIDs))
	for position, future := range resolves {
		encoded, err := future.Get()
		if err != nil {
			return nil, searchReadFailure(ctx, "read node resolution "+nodeIDs[position].String()+" for rescan", err)
		}
		if len(encoded) == 0 {
			continue
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return nil, searchReadFailure(ctx, "decode node resolution "+nodeIDs[position].String()+" for rescan", err)
		}
		values[position] = tr.Get(fdb.Key(nodeInstanceKey(resolved.OrgID, resolved.NodeType, nodeIDs[position])))
	}
	for position, future := range values {
		if future == nil {
			continue
		}
		encoded, err := future.Get()
		if err != nil {
			return nil, searchReadFailure(ctx, "read node "+nodeIDs[position].String()+" for rescan", err)
		}
		if len(encoded) == 0 {
			continue
		}
		var current node.Node
		if err := json.Unmarshal(encoded, &current); err != nil {
			return nil, searchReadFailure(ctx, "decode node "+nodeIDs[position].String()+" for rescan", err)
		}
		for _, name := range state.Names {
			if _, exists := current.Props[name]; exists {
				affected[nodeIDs[position]] = true
				break
			}
		}
	}
	return affected, nil
}
