package foundationdb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// nodeTypePageSize limits the number of node types in each ScanTypes page.
const nodeTypePageSize = 100

// ScanTypes reads a bounded page of node types across organizations in key
// order.
//
// It starts after cursor and returns the cursor for the following page.
//
//   - The last page returns an empty next cursor.
func (s *NodeTypeStore) ScanTypes(ctx context.Context, cursor string) (types []*node.NodeType, next string, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.scan")(&err)
	keyRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keyNodeTypeDef}.Pack()))
	if err != nil {
		return nil, "", nodeOperationFailure(ctx, "create the node type range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		key, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, "", nodeOperationFailure(ctx, "decode the node type cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(key))
	}
	var items []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "scan node types", func(tr fdb.Transaction) error {
		var readErr error
		selector := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
		items, readErr = tr.GetRange(selector, fdb.RangeOptions{Limit: nodeTypePageSize + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read node types", readErr)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	hasMore := len(items) > nodeTypePageSize
	if hasMore {
		items = items[:nodeTypePageSize]
	}
	types = make([]*node.NodeType, 0, len(items))
	for _, item := range items {
		var nodeType node.NodeType
		if err := json.Unmarshal(item.Value, &nodeType); err != nil {
			return nil, "", nodeOperationFailure(ctx, "decode a scanned node type", err)
		}
		types = append(types, &nodeType)
	}
	if hasMore {
		next = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	return types, next, nil
}

// Replace compares the decoded and re-encoded stored node type with before.
//
// If they match, it applies the top-level field changes between before and
// after and returns true. It preserves every other stored field's bytes and
// order. The same transaction updates the type key index and metadata epoch
// and writes a change event when the stored and replacement records differ.
//
//   - A mismatch returns false without writing changes.
func (s *NodeTypeStore) Replace(ctx context.Context, before, after *node.NodeType) (replaced bool, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.replace")(&err)
	expected, err := json.Marshal(before)
	if err != nil {
		return false, nodeOperationFailure(ctx, "encode expected node type "+before.ID.String(), err)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		return false, nodeOperationFailure(ctx, "encode replacement node type "+after.ID.String(), err)
	}
	changes, err := beginMutationAudit(ctx, after.OrgID)
	if err != nil {
		return false, err
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := nodeTypeDefKey(after.OrgID, after.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read node type "+after.ID.String(), readErr)
		}
		current, matchErr := storedNodeTypeEquals(ctx, previous, expected)
		replaced = current
		if matchErr != nil || !current {
			return matchErr
		}
		patched, patchErr := patchedNodeType(ctx, previous, expected, encoded)
		if patchErr != nil {
			return patchErr
		}
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, after.OrgID, after.ID, previous, after); err != nil {
			return err
		}
		tr.Set(fdb.Key(key), patched)
		return recordMetadataChange(ctx, tr, changes, nodeTypeTarget(after.ID, after.TypeKey), previous, patched)
	})
	if err != nil {
		return false, err
	}
	changes.finish(ctx)
	return replaced, nil
}

// storedNodeTypeEquals decodes stored as a node type and re-encodes it as
// JSON to compare it with expected. A missing record does not match.
func storedNodeTypeEquals(ctx context.Context, stored, expected []byte) (bool, error) {
	if len(stored) == 0 {
		return false, nil
	}
	var decoded node.NodeType
	if err := json.Unmarshal(stored, &decoded); err != nil {
		return false, searchReadFailure(ctx, "decode stored node type", err)
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		wrapped := fmt.Errorf("encode stored node type: %w", err)
		slog.ErrorContext(ctx, "store.node_type.replace_failed", slog.String("err", wrapped.Error()))
		return false, wrapped
	}
	return bytes.Equal(normalized, expected), nil
}
