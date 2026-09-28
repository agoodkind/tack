package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// nodeContentSnapshot stores every value one page read uses. One
// FoundationDB transaction reads the values with point reads and bounded
// index reads. The definitions field contains only the definitions that
// match the node's property names.
type nodeContentSnapshot struct {
	node         node.Node
	definitions  []*node.PropertyDef
	generation   int64
	revision     int64
	access       node.SearchAccess
	projectionID string
}

func (s *NodeContentStore) readSnapshot(ctx context.Context, nodeID uuid.UUID) (nodeContentSnapshot, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nodeContentSnapshot{}, searchReadFailure(ctx, "cancel content read", err)
		}
		transaction, err := s.db.CreateTransaction()
		if err != nil {
			return nodeContentSnapshot{}, searchReadFailure(ctx, "create content read transaction", err)
		}
		snapshot, readErr := readNodeContentSnapshot(ctx, transaction, nodeID)
		if readErr == nil {
			transaction.Cancel()
			return snapshot, nil
		}
		var databaseError fdb.Error
		if !errors.As(readErr, &databaseError) {
			transaction.Cancel()
			return nodeContentSnapshot{}, readErr
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			transaction.Cancel()
			return nodeContentSnapshot{}, searchReadFailure(ctx, "retry content read transaction", retryErr)
		}
		transaction.Cancel()
	}
}

func readNodeContentSnapshot(ctx context.Context, transaction fdb.Transaction, nodeID uuid.UUID) (nodeContentSnapshot, error) {
	resolveBytes, err := transaction.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return nodeContentSnapshot{}, searchReadFailure(ctx, "read node resolution", err)
	}
	if len(resolveBytes) == 0 {
		return nodeContentSnapshot{}, domain.ErrNotFound
	}
	var resolve node.NodeResolve
	if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
		return nodeContentSnapshot{}, searchReadFailure(ctx, "decode node resolution", err)
	}
	nodeBytes, err := transaction.Get(fdb.Key(nodeInstanceKey(resolve.OrgID, resolve.NodeType, nodeID))).Get()
	if err != nil {
		return nodeContentSnapshot{}, searchReadFailure(ctx, "read node value", err)
	}
	if len(nodeBytes) == 0 {
		return nodeContentSnapshot{}, domain.ErrNotFound
	}
	var current node.Node
	if err := json.Unmarshal(nodeBytes, &current); err != nil {
		return nodeContentSnapshot{}, searchReadFailure(ctx, "decode node value", err)
	}
	names := make([]string, 0, len(current.Props))
	for name := range current.Props {
		names = append(names, name)
	}
	slices.Sort(names)
	definitions, err := readDefinitionsByName(ctx, transaction, resolve.OrgID, names)
	if err != nil {
		return nodeContentSnapshot{}, err
	}
	generation, err := readSearchCounter(ctx, transaction, searchGenerationKey(resolve.OrgID, nodeID))
	if err != nil {
		return nodeContentSnapshot{}, err
	}
	revision, err := readSearchCounter(ctx, transaction, searchRevisionKey(resolve.OrgID, nodeID))
	if err != nil {
		return nodeContentSnapshot{}, err
	}
	access, err := accessStateFor(ctx, transaction, resolve.OrgID, nodeID)
	if err != nil {
		return nodeContentSnapshot{}, err
	}
	projectionID, err := projectionVersion(ctx, transaction, resolve.OrgID)
	if err != nil {
		return nodeContentSnapshot{}, err
	}
	return nodeContentSnapshot{
		node: current, definitions: definitions, generation: generation,
		revision: revision, access: access, projectionID: projectionID,
	}, nil
}
