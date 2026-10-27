package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// nodeTypeTarget returns the audit target of one node type definition with
// its type key as the name.
func nodeTypeTarget(typeID uuid.UUID, typeKey string) audit.Target {
	return nodeIdentityTarget(typeID, audit.TargetNodeTypeDefinition, typeKey)
}

// propertyDefTarget returns the audit target of one property definition with
// its property name as the name.
func propertyDefTarget(definitionID uuid.UUID, name string) audit.Target {
	return nodeIdentityTarget(definitionID, audit.TargetNodeTypePropertyDefinition, name)
}

// storedNodeTypeTarget returns the node audit target for typeID with NodeType "node_type".
// It sets Name to the TypeKey in previous, or to an empty string when previous is empty.
func storedNodeTypeTarget(ctx context.Context, typeID uuid.UUID, previous []byte) (audit.Target, error) {
	if len(previous) == 0 {
		return nodeTypeTarget(typeID, ""), nil
	}
	var stored node.NodeType
	if err := json.Unmarshal(previous, &stored); err != nil {
		return audit.Target{}, searchReadFailure(ctx, "decode node type "+typeID.String(), err)
	}
	return nodeTypeTarget(typeID, stored.TypeKey), nil
}

// storedPropertyDefTarget returns the node audit target for defID with NodeType "property_def".
// It sets Name to the property name in previous, or to an empty string when previous is empty.
func storedPropertyDefTarget(ctx context.Context, defID uuid.UUID, previous []byte) (audit.Target, error) {
	if len(previous) == 0 {
		return propertyDefTarget(defID, ""), nil
	}
	var stored node.PropertyDef
	if err := json.Unmarshal(previous, &stored); err != nil {
		return audit.Target{}, searchReadFailure(ctx, "decode property definition "+defID.String(), err)
	}
	return propertyDefTarget(defID, stored.Name), nil
}

// recordMetadataChange records the change of one metadata record with its
// stored and written values and writes the change events of the attempt to
// the outbox in tr.
func recordMetadataChange(ctx context.Context, tr fdb.Transaction, changes *mutationAudit, target audit.Target, before, after []byte) error {
	if err := changes.record(target, before, after); err != nil {
		return nodeOperationFailure(ctx, "record the change of "+target.NodeType+" "+target.ID.String(), err)
	}
	return changes.append(ctx, tr)
}
