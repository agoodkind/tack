package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const relationViewPeerIndex = 4

func readRelationView(
	ctx context.Context,
	transaction fdb.ReadTransaction,
	orgID uuid.UUID,
	nodeType string,
	item fdb.KeyValue,
) (*node.NodeView, error) {
	peerID, valid := decodeCandidateViewID(item.Key, relationViewPeerIndex, relationViewPeerIndex+1)
	if !valid {
		return nil, nil
	}

	encoded, err := transaction.Get(fdb.Key(nodeResolveKey(peerID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "resolve relationship list node "+peerID.String(), err)
	}

	if len(encoded) == 0 {
		return nil, nil
	}

	resolved, valid := decodeCandidateResolution(encoded)
	if !valid {
		return nil, nil
	}

	if resolved.OrgID != orgID || (nodeType != "" && resolved.NodeType != nodeType) {
		return nil, nil
	}

	encoded, err = transaction.Get(fdb.Key(nodeViewKey(orgID, resolved.NodeType, peerID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read relationship list view "+peerID.String(), err)
	}

	if len(encoded) == 0 {
		return nil, nil
	}

	view, valid := decodeCandidateView(encoded)
	if !valid {
		return nil, nil
	}

	if view.OrgID != orgID || view.NodeType != resolved.NodeType || view.ID != peerID {
		return nil, nil
	}

	return &view, nil
}

func decodeCandidateResolution(encoded []byte) (node.NodeResolve, bool) {
	var resolved node.NodeResolve
	valid := json.Unmarshal(encoded, &resolved) == nil

	return resolved, valid
}
