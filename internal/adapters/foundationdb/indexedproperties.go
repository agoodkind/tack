package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const (
	propertyViewTupleSize = 6
	propertyViewNodeIndex = 5
)

func readPropertyView(
	ctx context.Context,
	transaction fdb.ReadTransaction,
	orgID uuid.UUID,
	nodeType string,
	item fdb.KeyValue,
) (*node.NodeView, error) {
	nodeID, valid := decodeCandidateViewID(item.Key, propertyViewNodeIndex, propertyViewTupleSize)
	if !valid {
		return nil, nil
	}

	encoded, err := transaction.Get(fdb.Key(nodeViewKey(orgID, nodeType, nodeID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read property list view "+nodeID.String(), err)
	}

	if len(encoded) == 0 {
		return nil, nil
	}

	view, valid := decodeCandidateView(encoded)
	if !valid {
		return nil, nil
	}

	return &view, nil
}

func decodeCandidateViewID(key fdb.Key, fieldIndex int, minimumFields int) (uuid.UUID, bool) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil || len(values) < minimumFields {
		return uuid.Nil, false
	}

	nodeIDText, isText := values[fieldIndex].(string)
	if !isText {
		return uuid.Nil, false
	}

	nodeID, err := uuid.Parse(nodeIDText)

	return nodeID, err == nil
}

func decodeCandidateView(encoded []byte) (node.NodeView, bool) {
	var view node.NodeView
	valid := json.Unmarshal(encoded, &view) == nil

	return view, valid
}
