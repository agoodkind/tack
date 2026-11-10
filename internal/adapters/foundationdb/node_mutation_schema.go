package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// readSchemaSnapshot reads the metadata epoch, property definitions, and node
// types for orgID inside tr.
func readSchemaSnapshot(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (node.SchemaSnapshot, error) {
	empty := node.SchemaSnapshot{Schema: node.Schema{OrgID: orgID, Types: nil, Definitions: nil}, MetadataEpoch: 0}
	epoch, err := readMetadataEpoch(ctx, tr, orgID)
	if err != nil {
		return empty, err
	}
	definitions, err := readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(orgID), "property definitions")
	if err != nil {
		return empty, err
	}
	types, err := readSchemaRecords[node.NodeType](ctx, tr, nodeTypeDefPrefix(orgID), "node types")
	if err != nil {
		return empty, err
	}
	return node.SchemaSnapshot{
		Schema:        node.Schema{OrgID: orgID, Types: node.BuildTypeIndex(types), Definitions: definitions},
		MetadataEpoch: epoch,
	}, nil
}

// readMetadataEpoch reads the metadata epoch for orgID inside tr. It returns
// epoch zero if the counter does not exist.
func readMetadataEpoch(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (int64, error) {
	return decodeEpochValue(ctx, tr.Get(fdb.Key(searchEpochKey(orgID))))
}

// readSchemaRecords reads and decodes every record under prefix inside tr.
func readSchemaRecords[Record node.PropertyDef | node.NodeType](ctx context.Context, tr fdb.Transaction, prefix []byte, subject string) ([]*Record, error) {
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the range of "+subject, err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read "+subject, err)
	}
	records := make([]*Record, 0, len(items))
	for _, item := range items {
		var record Record
		if err := json.Unmarshal(item.Value, &record); err != nil {
			return nil, nodeOperationFailure(ctx, "decode "+subject, err)
		}
		records = append(records, &record)
	}
	return records, nil
}

// indexedPropertyNames returns the names of indexed definitions in schema
// that apply to kind.
func indexedPropertyNames(schema node.Schema, kind *node.NodeType) []string {
	names := make([]string, 0, len(schema.Definitions))
	for _, definition := range schema.Definitions {
		if definition.Indexed && node.PropertyAppliesTo(definition, kind) {
			names = append(names, definition.Name)
		}
	}
	return names
}
