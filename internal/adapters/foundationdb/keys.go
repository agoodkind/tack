package foundationdb

import (
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

	// Forward relationship.
	// (relationship, orgID, sourceID, relationType, targetID) -> metadata JSON
	keyRelationship = "relationship"

	// Reverse relationship for lookups by target.
	// (relationship_reverse, orgID, targetID, relationType, sourceID) -> nil
	keyRelationshipReverse = "relationship_reverse"

	// Global slug index for entry-point nodes (workspaces, orgs).
	// (slug_index, nodeType, slug) -> nodeID bytes
	keySlugIndex = "slug_index"

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
)

// nodeInstanceKey packs a primary node key.
func nodeInstanceKey(orgID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeInstance, orgID.String(), nodeType, nodeID.String()}.Pack()
}

// nodeViewKey packs a materialized view key.
func nodeViewKey(orgID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeView, orgID.String(), nodeType, nodeID.String()}.Pack()
}

// nodeResolveKey packs a global resolve key.
func nodeResolveKey(nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeResolve, nodeID.String()}.Pack()
}

// nodeByPropertyKey packs a secondary property index key.
func nodeByPropertyKey(orgID uuid.UUID, nodeType, propName string, encodedValue []byte, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeByProperty, orgID.String(), nodeType, propName, encodedValue, nodeID.String()}.Pack()
}

// relationshipKey packs a forward relationship key.
func relationshipKey(orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) []byte {
	return tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType, targetID.String()}.Pack()
}

// relationshipReverseKey packs a reverse relationship key.
func relationshipReverseKey(orgID, targetID uuid.UUID, relationType string, sourceID uuid.UUID) []byte {
	return tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType, sourceID.String()}.Pack()
}

// slugIndexKey packs a global slug key.
func slugIndexKey(nodeType, slug string) []byte {
	return tuple.Tuple{keySlugIndex, nodeType, slug}.Pack()
}

// sequenceKey packs an atomic sequence counter key.
func sequenceKey(orgID, scopeNodeID uuid.UUID, nodeType string) []byte {
	return tuple.Tuple{keySequence, orgID.String(), scopeNodeID.String(), nodeType}.Pack()
}

// nodeTypeDefKey packs a NodeType config key.
func nodeTypeDefKey(orgID, typeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeTypeDef, orgID.String(), typeID.String()}.Pack()
}

// propertyDefKey packs a PropertyDef key.
func propertyDefKey(orgID, defID uuid.UUID) []byte {
	return tuple.Tuple{keyPropertyDef, orgID.String(), defID.String()}.Pack()
}

// nodeViewPrefix packs the prefix for scanning views of (orgID, nodeType).
func nodeViewPrefix(orgID uuid.UUID, nodeType string) []byte {
	return tuple.Tuple{keyNodeView, orgID.String(), nodeType}.Pack()
}

// nodeByPropertyValuePrefix packs the prefix for scanning the property index
// narrowed to a specific encoded value.
func nodeByPropertyValuePrefix(orgID uuid.UUID, nodeType, propName string, encodedValue []byte) []byte {
	return tuple.Tuple{keyNodeByProperty, orgID.String(), nodeType, propName, encodedValue}.Pack()
}

// relationshipPrefixBySource packs the prefix for listing all relationships
// from sourceID, optionally narrowed to a specific relationType.
func relationshipPrefixBySource(orgID, sourceID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return tuple.Tuple{keyRelationship, orgID.String(), sourceID.String()}.Pack()
	}
	return tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType}.Pack()
}

// relationshipReversePrefixByTarget packs the prefix for listing all relationships
// pointing to targetID, optionally narrowed to a specific relationType.
func relationshipReversePrefixByTarget(orgID, targetID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String()}.Pack()
	}
	return tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType}.Pack()
}

// nodeTypeDefPrefix packs the prefix for scanning all NodeType records in an org.
func nodeTypeDefPrefix(orgID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeTypeDef, orgID.String()}.Pack()
}

// propertyDefPrefix packs the prefix for scanning all PropertyDef records in an org.
func propertyDefPrefix(orgID uuid.UUID) []byte {
	return tuple.Tuple{keyPropertyDef, orgID.String()}.Pack()
}
