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

// ScanTypes reads one bounded page of node types across all organizations in
// key order. It returns an empty next cursor for the last page.
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

// Replace decodes and re-encodes the stored node type before comparing it
// with before. If the values match, Replace stores after and returns true.
// The same transaction updates the type-key index and metadata epoch and
// writes the audit event staged on ctx. If the values do not match, Replace
// does not write and returns false.
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
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
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
		if err := updateNodeTypeIndex(ctx, tr, s.clock.Now(), s.searchWork, after.OrgID, after.ID, previous, after); err != nil {
			return err
		}
		tr.Set(fdb.Key(key), encoded)
		return writeStagedIntent(ctx, tr)
	})
	if err != nil {
		return false, err
	}
	if replaced {
		commitStagedIntent(ctx)
	}
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
