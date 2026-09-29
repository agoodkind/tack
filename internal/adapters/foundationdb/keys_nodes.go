package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// nodeInstanceKey packs a primary node key.
func nodeInstanceKey(orgID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeInstance, orgID.String(), nodeType, nodeID.String()}.Pack())
}

// nodeViewKey packs a materialized view key.
func nodeViewKey(orgID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeView, orgID.String(), nodeType, nodeID.String()}.Pack())
}

// nodeResolveKey packs a global resolve key.
func nodeResolveKey(nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeResolve, nodeID.String()}.Pack())
}

// nodeByPropertyKey packs a secondary property index key.
func nodeByPropertyKey(orgID uuid.UUID, nodeType, propName string, encodedValue []byte, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeByProperty, orgID.String(), nodeType, propName, encodedValue, nodeID.String()}.Pack())
}

// nodeReferenceKey packs a forward reference ownership key.
func nodeReferenceKey(orgID uuid.UUID, templateName, encoded string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReference, orgID.String(), templateName, encoded}.Pack())
}

// nodeReferenceOwnedKey packs a reverse reference ownership key.
func nodeReferenceOwnedKey(orgID, nodeID uuid.UUID, templateName string) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String(), templateName}.Pack())
}

// nodeReferenceOwnedPrefix packs a node's reverse reference ownership prefix.
func nodeReferenceOwnedPrefix(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeReferenceOwned, orgID.String(), nodeID.String()}.Pack())
}

// nodeTypeDefKey packs a NodeType config key.
func nodeTypeDefKey(orgID, typeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeDef, orgID.String(), typeID.String()}.Pack())
}

// nodeTypeByKeyPrefix packs the prefix of the node types that use typeKey.
func nodeTypeByKeyPrefix(orgID uuid.UUID, typeKey string) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeByKey, orgID.String(), typeKey}.Pack())
}

// nodeTypeByKeyKey packs one type-key index entry.
func nodeTypeByKeyKey(orgID uuid.UUID, typeKey string, typeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeByKey, orgID.String(), typeKey, typeID.String()}.Pack())
}

// propertyDefKey packs a PropertyDef key.
func propertyDefKey(orgID, defID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDef, orgID.String(), defID.String()}.Pack())
}

// propertyDefByNamePrefix packs the prefix of the definitions that use name.
func propertyDefByNamePrefix(orgID uuid.UUID, name string) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDefByName, orgID.String(), name}.Pack())
}

// propertyDefByNameKey packs one property-name index entry.
func propertyDefByNameKey(orgID uuid.UUID, name string, defID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDefByName, orgID.String(), name, defID.String()}.Pack())
}

// nodeDeleteJobKey packs the record key of one subtree delete job.
func nodeDeleteJobKey(jobID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeDeleteJob, jobID.String()}.Pack())
}

// nodeDeleteJobPrefix packs the prefix of every subtree delete job record.
func nodeDeleteJobPrefix() []byte {
	return withPrefix(tuple.Tuple{keyNodeDeleteJob}.Pack())
}

// idempotencyKey packs the per-org idempotency-key sentinel.
func idempotencyKey(orgID uuid.UUID, key string) []byte {
	return withPrefix(tuple.Tuple{keyIdempotency, orgID.String(), key}.Pack())
}
