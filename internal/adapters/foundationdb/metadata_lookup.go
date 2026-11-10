package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// maxIndexedMatches is the range limit of one metadata index lookup. The
// lookup reads up to two entries. A second entry reveals a duplicate type
// key or property name.
const maxIndexedMatches = 2

// TypeByKey reads the node type of orgID that uses typeKey. It performs one
// bounded range read of the type-key index and one point read. It returns nil
// when no node type uses the key and an error when several do.
func (s *NodeTypeStore) TypeByKey(ctx context.Context, orgID uuid.UUID, typeKey string) (out *node.NodeType, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.by_key")(&err)
	var encoded []byte
	err = runNodeReadTransaction(ctx, s.db, "node_type.by_key", func(tr fdb.Transaction) error {
		typeIDs, readErr := indexedIDs(ctx, tr, nodeTypeByKeyPrefix(orgID, typeKey))
		if readErr != nil || len(typeIDs) == 0 {
			return readErr
		}
		if len(typeIDs) > 1 {
			return fmt.Errorf("node type key %q is used by several node types", typeKey)
		}
		encoded, readErr = tr.Get(fdb.Key(nodeTypeDefKey(orgID, typeIDs[0]))).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read node type "+typeIDs[0].String(), readErr)
		}
		return nil
	})
	if err != nil || len(encoded) == 0 {
		return nil, err
	}
	var kind node.NodeType
	if err := json.Unmarshal(encoded, &kind); err != nil {
		return nil, nodeOperationFailure(ctx, "decode node type "+typeKey, err)
	}
	if kind.TypeKey != typeKey {
		return nil, nil
	}
	return &kind, nil
}

// readDefinitionsByName reads the property definitions that use each name
// through the property-name index. Each name requires one range read of at
// most two entries and one point read per entry. The read count depends on
// the number of names and not on the organization's definition count. A
// missing name returns no definition, and a duplicated name returns both.
// The text projection rejects either case.
func readDefinitionsByName(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, names []string) ([]*node.PropertyDef, error) {
	definitions := make([]*node.PropertyDef, 0, len(names))
	for _, name := range names {
		definitionIDs, err := indexedIDs(ctx, tr, propertyDefByNamePrefix(orgID, name))
		if err != nil {
			return nil, err
		}
		for _, definitionID := range definitionIDs {
			encoded, err := tr.Get(fdb.Key(propertyDefKey(orgID, definitionID))).Get()
			if err != nil {
				return nil, searchReadFailure(ctx, "read property definition "+definitionID.String(), err)
			}
			if len(encoded) == 0 {
				continue
			}
			var definition node.PropertyDef
			if err := json.Unmarshal(encoded, &definition); err != nil {
				return nil, searchReadFailure(ctx, "decode property definition "+definitionID.String(), err)
			}
			if definition.Name == name {
				definitions = append(definitions, &definition)
			}
		}
	}
	return definitions, nil
}

// indexedIDs reads at most maxIndexedMatches IDs from the last tuple element
// of the index entries under prefix.
func indexedIDs(ctx context.Context, tr fdb.Transaction, prefix []byte) ([]uuid.UUID, error) {
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, searchReadFailure(ctx, "create metadata index range", err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: maxIndexedMatches}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read metadata index", err)
	}
	identifiers := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		identifier, err := lastTupleID(ctx, item.Key)
		if err != nil {
			return nil, err
		}
		identifiers = append(identifiers, identifier)
	}
	return identifiers, nil
}

// lastTupleID decodes the UUID string in the last tuple element of key.
func lastTupleID(ctx context.Context, key fdb.Key) (uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "unpack index key", err)
	}
	if len(values) == 0 {
		return uuid.Nil, searchReadFailure(ctx, "decode index key", errors.New("index key has no tuple elements"))
	}
	text, isText := values[len(values)-1].(string)
	if !isText {
		return uuid.Nil, searchReadFailure(ctx, "decode index key", errors.New("index key identifier is not a string"))
	}
	identifier, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "parse index key identifier", err)
	}
	return identifier, nil
}
