package foundationdb

import (
	"bytes"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// FDB key space. Everything is expressed through one of a small set of generic
// patterns; nothing in the key space privileges a specific concept (no
// assignment keys, no label keys, no comment keys, etc.).
//
// All keys use the tuple layer. orgID sits early in the tuple for tenant
// locality.
const (
	// Primary node storage.
	// (node_instance, orgID, nodeType, nodeID) -> Node JSON
	keyNodeInstance = "node_instance"

	// Materialized read view. Mirrors node_instance key structure.
	// (node_view, orgID, nodeType, nodeID) -> NodeView JSON
	keyNodeView = "node_view"

	// Global resolution record; NOT org-scoped. Keyed by nodeID only so any
	// caller can resolve an entity without knowing its org upfront.
	// (node_resolve, nodeID) -> NodeResolve JSON
	keyNodeResolve = "node_resolve"

	// Secondary index for indexed PropertyDefs. Sorted by encoded value so
	// filtered range scans are cheap.
	// (node_by_property, orgID, nodeType, propName, encodedValue, nodeID) -> nil
	keyNodeByProperty = "node_by_property"

	// Forward node reference uniqueness index.
	// (node_reference, orgID, templateName, encoded) -> nodeID
	keyNodeReference = "node_reference"

	// Reverse node reference ownership index.
	// (node_reference_owned, orgID, nodeID, templateName) -> encoded
	keyNodeReferenceOwned = "node_reference_owned"

	// Forward relationship.
	// (relationship, orgID, sourceID, relationType, targetID) -> metadata JSON
	keyRelationship = "relationship"

	// Reverse relationship for lookups by target.
	// (relationship_reverse, orgID, targetID, relationType, sourceID) -> nil
	keyRelationshipReverse = "relationship_reverse"

	// Atomic sequence counters keyed by (orgID, scopeNodeID, nodeType).
	// scopeNodeID is the container that defines uniqueness (typically a project).
	// (sequence, orgID, scopeNodeID, nodeType) -> int64
	keySequence = "sequence"

	// NodeType configuration records.
	// (node_type_def, orgID, typeID) -> NodeType JSON
	keyNodeTypeDef = "node_type_def"

	// PropertyDef records.
	// (property_def, orgID, defID) -> PropertyDef JSON
	keyPropertyDef = "property_def"

	// Idempotency key index. The value is an IdempotencyRecord JSON payload.
	// Older records may contain only the raw nodeID bytes.
	// (idempotency_key, orgID, key) -> IdempotencyRecord JSON
	keyIdempotency = "idempotency_key"
)

// testPrefix is prepended to every packed FDB key when non-nil. Production
// callers leave it nil; integration tests set a per-test UUID via
// SetTestPrefix so parallel tests on a shared FDB cluster cannot collide.
//
// Set only via SetTestPrefix from the test setup helper.
var testPrefix []byte

// SetTestPrefix configures a per-test key prefix. Pass nil to clear. The
// prefix is encoded as a single bytes element in the tuple layer so range
// scans stay tuple-aware.
//
// Tests own this. Do not call from production code.
func SetTestPrefix(prefix []byte) {
	testPrefix = prefix
}

// withPrefix prepends the active test prefix (if any) to a packed key. The
// production fast path returns the input unchanged.
func withPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	out := make([]byte, 0, len(testPrefix)+len(key))
	out = append(out, testPrefix...)
	out = append(out, key...)
	return out
}

// stripPrefix removes the active test prefix from a key returned by an FDB
// range read. Production callers leave testPrefix nil and the function is a
// no-op. Stores must call this before tuple.Unpack on a returned key so the
// unpack starts at the real tuple bytes rather than the raw test prefix.
func stripPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	if len(key) >= len(testPrefix) && bytes.Equal(key[:len(testPrefix)], testPrefix) {
		return key[len(testPrefix):]
	}
	return key
}

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

// relationshipKey packs a forward relationship key.
func relationshipKey(orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType, targetID.String()}.Pack())
}

// relationshipReverseKey packs a reverse relationship key.
func relationshipReverseKey(orgID, targetID uuid.UUID, relationType string, sourceID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType, sourceID.String()}.Pack())
}

// sequenceKey packs an atomic sequence counter key.
func sequenceKey(orgID, scopeNodeID uuid.UUID, nodeType string) []byte {
	return withPrefix(tuple.Tuple{keySequence, orgID.String(), scopeNodeID.String(), nodeType}.Pack())
}

// nodeTypeDefKey packs a NodeType config key.
func nodeTypeDefKey(orgID, typeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeDef, orgID.String(), typeID.String()}.Pack())
}

// propertyDefKey packs a PropertyDef key.
func propertyDefKey(orgID, defID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDef, orgID.String(), defID.String()}.Pack())
}

// idempotencyKey packs the per-org idempotency-key sentinel.
func idempotencyKey(orgID uuid.UUID, key string) []byte {
	return withPrefix(tuple.Tuple{keyIdempotency, orgID.String(), key}.Pack())
}

// nodeViewPrefix packs the prefix for scanning views of (orgID, nodeType).
func nodeViewPrefix(orgID uuid.UUID, nodeType string) []byte {
	return withPrefix(tuple.Tuple{keyNodeView, orgID.String(), nodeType}.Pack())
}

// nodeByPropertyValuePrefix packs the prefix for scanning the property index
// narrowed to a specific encoded value.
func nodeByPropertyValuePrefix(orgID uuid.UUID, nodeType, propName string, encodedValue []byte) []byte {
	return withPrefix(tuple.Tuple{keyNodeByProperty, orgID.String(), nodeType, propName, encodedValue}.Pack())
}

// relationshipPrefixBySource packs the prefix for listing all relationships
// from sourceID, optionally narrowed to a specific relationType.
func relationshipPrefixBySource(orgID, sourceID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String()}.Pack())
	}
	return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType}.Pack())
}

// relationshipReversePrefixByTarget packs the prefix for listing all relationships
// pointing to targetID, optionally narrowed to a specific relationType.
func relationshipReversePrefixByTarget(orgID, targetID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String()}.Pack())
	}
	return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType}.Pack())
}

// nodeTypeDefPrefix packs the prefix for scanning all NodeType records in an org.
func nodeTypeDefPrefix(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeDef, orgID.String()}.Pack())
}

// propertyDefPrefix packs the prefix for scanning all PropertyDef records in an org.
func propertyDefPrefix(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDef, orgID.String()}.Pack())
}

// TestPrefixRange returns a range covering every key under the active test
// prefix. Tests use this to range-clear all data they wrote during a run.
// Returns nil when no test prefix is set, so production code is safe to call.
func TestPrefixRange() []byte {
	if testPrefix == nil {
		return nil
	}
	return testPrefix
}
