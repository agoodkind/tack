package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// SetReferenceKeys replaces the rendered references owned by a node.
func (s *NodeStore) SetReferenceKeys(ctx context.Context, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.set_reference_keys")(&err)
	err = runNodeMutation(ctx, s.db, "node.set_reference_keys", func(tr fdb.Transaction) error {
		return writeReferenceKeys(tr, orgID, nodeID, keys)
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return err
		}
		wrapped := fmt.Errorf("set references for node %s: %w", nodeID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.reference.set_failed", slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
		return wrapped
	}
	return nil
}

// EnsurePropertyIndex writes secondary-index entries for the selected properties.
func (s *NodeStore) EnsurePropertyIndex(ctx context.Context, current *node.Node, indexedProps []string) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.ensure_property_index")(&err)
	err = runNodeMutation(ctx, s.db, "node.ensure_property_index", func(tr fdb.Transaction) error {
		writeCreatePropertyIndexes(tr, current, indexedProps)
		return nil
	})
	if err != nil {
		return nodeOperationFailure(ctx, "ensure property index", err)
	}
	return nil
}

// ListByProperty scans a secondary index and reads the matching node records.
func (s *NodeStore) ListByProperty(ctx context.Context, orgID uuid.UUID, nodeType, propName string, value json.RawMessage) (nodes []*node.Node, err error) {
	defer telemetry.FDBOp(ctx, "store.node.list_by_property")(&err)
	keyRange, err := fdb.PrefixRange(nodeByPropertyValuePrefix(orgID, nodeType, propName, encodePropertyValue(value)))
	if err != nil {
		wrapped := fmt.Errorf("create property index range: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.list_by_property_range_failed", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
		return nil, wrapped
	}
	var result []*node.Node
	err = runNodeReadTransaction(ctx, s.db, "node.list_by_property", func(tr fdb.Transaction) error {
		var readErr error
		result, readErr = readIndexedNodes(ctx, tr, keyRange, orgID, nodeType)
		return readErr
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return nil, err
		}
		wrapped := fmt.Errorf("fdb list by property: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.list_by_property_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return result, nil
}

func readIndexedNodes(ctx context.Context, tr fdb.Transaction, keyRange fdb.Range, orgID uuid.UUID, nodeType string) ([]*node.Node, error) {
	indexItems, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		wrapped := fmt.Errorf("read property index range: %w", err)
		return nil, nodeOperationFailure(ctx, "read property index", wrapped)
	}
	result := make([]*node.Node, 0, len(indexItems))
	for _, item := range indexItems {
		values, unpackErr := tuple.Unpack(stripPrefix(item.Key))
		if unpackErr != nil || len(values) < 6 {
			continue
		}
		nodeIDText, _ := values[5].(string)
		nodeID, parseErr := uuid.Parse(nodeIDText)
		if parseErr != nil {
			continue
		}
		encodedNode, readErr := tr.Get(fdb.Key(nodeInstanceKey(orgID, nodeType, nodeID))).Get()
		if readErr != nil {
			wrapped := fmt.Errorf("read indexed node %s: %w", nodeID, readErr)
			return nil, nodeOperationFailure(ctx, "read indexed node", wrapped)
		}
		if len(encodedNode) == 0 {
			continue
		}
		var current node.Node
		if decodeErr := json.Unmarshal(encodedNode, &current); decodeErr != nil {
			continue
		}
		result = append(result, &current)
	}
	return result, nil
}
