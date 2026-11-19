package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

var errDuplicateNodeTypeKey = errors.New("is used by several node types")

// cachedNodeTypeByKey reads the node type of orgID that uses typeKey through
// the type-key index. A missing type yields nil, and a type key that several
// node types use is an error.
func cachedNodeTypeByKey(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	typeKey string,
	kinds map[string]*node.NodeType,
) (*node.NodeType, error) {
	if kind, cached := kinds[typeKey]; cached {
		return kind, nil
	}

	typeIDs, err := indexedIDs(ctx, transaction, nodeTypeByKeyPrefix(orgID, typeKey))
	if err != nil {
		return nil, err
	}

	if len(typeIDs) > 1 {
		return nil, nodeOperationFailure(
			ctx,
			"read node type "+typeKey,
			fmt.Errorf("node type key %q %w", typeKey, errDuplicateNodeTypeKey),
		)
	}

	kinds[typeKey] = nil
	if len(typeIDs) == 0 {
		return nil, nil
	}

	encoded, err := transaction.Get(fdb.Key(nodeTypeDefKey(orgID, typeIDs[0]))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read node type "+typeIDs[0].String(), err)
	}

	if len(encoded) == 0 {
		return nil, nil
	}

	var decoded node.NodeType
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, searchReadFailure(ctx, "decode node type "+typeKey, err)
	}

	if decoded.TypeKey != typeKey {
		return nil, nil
	}

	kinds[typeKey] = &decoded

	return &decoded, nil
}
