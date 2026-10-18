package foundationdb

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// nodesWithNames reports which nodes in nodeIDs match the scan's content
// selection. A node matches if it has a value under one of the scan's
// property names. It also matches if it has one of the scan's type keys
// and a value under a definition that applies to one of the scan's
// features. It does not read when the scan does not request content or
// selects every node. It first issues every resolution read. It then
// waits for each resolution read in order and issues the corresponding
// node read. It waits for node reads after issuing all of them.
func nodesWithNames(ctx context.Context, tr fdb.Transaction, nodeIDs []uuid.UUID, state searchScanRecord) (map[uuid.UUID]bool, error) {
	affected := make(map[uuid.UUID]bool, len(nodeIDs))
	if !state.Content || allContent(state) {
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
		selected, err := nodeSelected(ctx, tr, current, state)
		if err != nil {
			return nil, err
		}
		affected[nodeIDs[position]] = selected
	}
	return affected, nil
}

// nodeSelected reports whether current matches the scan's property names
// or type changes. For a matching type key, it reads definitions for the
// node's property names through the property-name index and checks their
// applicability against the scan's changed features.
func nodeSelected(ctx context.Context, tr fdb.Transaction, current node.Node, state searchScanRecord) (bool, error) {
	for _, name := range state.Names {
		if _, exists := current.Props[name]; exists {
			return true, nil
		}
	}
	if !slices.Contains(state.TypeKeys, current.NodeType) {
		return false, nil
	}
	names := make([]string, 0, len(current.Props))
	for name := range current.Props {
		names = append(names, name)
	}
	slices.Sort(names)
	definitions, err := readDefinitionsByName(ctx, tr, current.OrgID, names)
	if err != nil {
		return false, err
	}
	for _, definition := range definitions {
		for _, feature := range definition.AppliesToFeatures {
			if slices.Contains(state.Features, feature) {
				return true, nil
			}
		}
	}
	return false, nil
}
