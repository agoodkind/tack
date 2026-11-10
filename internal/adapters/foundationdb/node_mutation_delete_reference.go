package foundationdb

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// refuseReferencedNode returns an error that wraps
// domain.ErrFailedPrecondition if a node in the organization references
// source through an indexed UUID property definition that uses source's node
// type as its reference target type. It reads one property index entry for
// each such definition and each node type to which the definition applies.
func refuseReferencedNode(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, source mutationSource) error {
	definitions, err := readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(orgID), "property definitions")
	if err != nil {
		return err
	}
	kinds, err := readSchemaRecords[node.NodeType](ctx, tr, nodeTypeDefPrefix(orgID), "node types")
	if err != nil {
		return err
	}
	value := encodePropertyValue(json.RawMessage(strconv.Quote(source.Node.ID.String())))
	for _, definition := range definitions {
		if definition.Type != node.PropertyTypeUUID || !definition.Indexed || definition.ReferenceTargetTypeKey != source.TypeKey {
			continue
		}
		for _, kind := range kinds {
			if !node.PropertyAppliesTo(definition, kind) {
				continue
			}
			keyRange, err := fdb.PrefixRange(nodeByPropertyValuePrefix(orgID, kind.TypeKey, definition.Name, value))
			if err != nil {
				return nodeOperationFailure(ctx, "create the "+definition.Name+" index range", err)
			}
			entries, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
			if err != nil {
				return searchReadFailure(ctx, "read the "+definition.Name+" index of "+kind.TypeKey+" nodes", err)
			}
			if len(entries) > 0 {
				reason := "a " + kind.TypeKey + " node references the node through " + definition.Name
				return mutationRefusal(ctx, source.Node.ID, reason, domain.ErrFailedPrecondition)
			}
		}
	}
	return nil
}
