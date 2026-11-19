package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func decodeRelationPageEntry(
	ctx context.Context,
	transaction fdb.ReadTransaction,
	query node.NodeListQuery,
	item fdb.KeyValue,
) (pageEntry, error) {
	nodeID, err := edgeEnd(ctx, item.Key)
	if err != nil {
		return pageEntry{}, err
	}

	entry := pageEntry{id: nodeID, view: nil, key: item.Key}
	encoded, err := transaction.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return entry, searchReadFailure(ctx, "resolve relationship list node "+nodeID.String(), err)
	}

	if len(encoded) == 0 {
		return entry, nil
	}

	var resolved node.NodeResolve
	if err := json.Unmarshal(encoded, &resolved); err != nil {
		return entry, nodeOperationFailure(
			ctx,
			"decode relationship list node "+nodeID.String(),
			err,
		)
	}

	if resolved.OrgID != query.OrgID ||
		(query.NodeType != "" && resolved.NodeType != query.NodeType) {
		return entry, nil
	}

	encoded, err = transaction.Get(
		fdb.Key(nodeViewKey(query.OrgID, resolved.NodeType, nodeID)),
	).Get()
	if err != nil {
		return entry, searchReadFailure(ctx, "read relationship list view "+nodeID.String(), err)
	}

	if len(encoded) == 0 {
		return entry, nil
	}

	var view node.NodeView
	if err := json.Unmarshal(encoded, &view); err != nil {
		return entry, nodeOperationFailure(
			ctx,
			"decode relationship list view "+nodeID.String(),
			err,
		)
	}

	if view.OrgID != query.OrgID || view.NodeType != resolved.NodeType || view.ID != nodeID {
		return entry, nil
	}

	entry.view = &view

	return entry, nil
}
