package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// relationshipKey packs a forward relationship key.
func relationshipKey(orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType, targetID.String()}.Pack())
}

// relationshipReverseKey packs a reverse relationship key.
func relationshipReverseKey(orgID, targetID uuid.UUID, relationType string, sourceID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType, sourceID.String()}.Pack())
}

// relationshipPrefixBySource packs the prefix for listing all relationships
// from sourceID, optionally narrowed to a specific relationType.
func relationshipPrefixBySource(orgID, sourceID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String()}.Pack())
	}
	return withPrefix(tuple.Tuple{keyRelationship, orgID.String(), sourceID.String(), relationType}.Pack())
}

// relationshipReversePrefixByTarget packs the prefix for listing relationships
// pointing to targetID, optionally narrowed to a specific relationType.
func relationshipReversePrefixByTarget(orgID, targetID uuid.UUID, relationType string) []byte {
	if relationType == "" {
		return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String()}.Pack())
	}
	return withPrefix(tuple.Tuple{keyRelationshipReverse, orgID.String(), targetID.String(), relationType}.Pack())
}

// nodeViewPrefix packs the prefix for scanning views of (orgID, nodeType).
func nodeViewPrefix(orgID uuid.UUID, nodeType string) []byte {
	return withPrefix(tuple.Tuple{keyNodeView, orgID.String(), nodeType}.Pack())
}

// nodeInstanceOrgPrefix packs the prefix for scanning every node of orgID.
func nodeInstanceOrgPrefix(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeInstance, orgID.String()}.Pack())
}

// nodeByPropertyValuePrefix packs the prefix for scanning the property index
// narrowed to a specific encoded value.
func nodeByPropertyValuePrefix(orgID uuid.UUID, nodeType, propName string, encodedValue []byte) []byte {
	return withPrefix(tuple.Tuple{keyNodeByProperty, orgID.String(), nodeType, propName, encodedValue}.Pack())
}

func sequenceKey(orgID, scopeNodeID uuid.UUID, nodeType string) []byte {
	return withPrefix(tuple.Tuple{keySequence, orgID.String(), scopeNodeID.String(), nodeType}.Pack())
}

func sequenceByKeyKey(orgID uuid.UUID, counterKey string) []byte {
	return withPrefix(tuple.Tuple{keySequence, orgID.String(), counterKey}.Pack())
}

func nodeTypeDefPrefix(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyNodeTypeDef, orgID.String()}.Pack())
}

func propertyDefPrefix(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keyPropertyDef, orgID.String()}.Pack())
}

func opsOutboxPrefix() []byte {
	return withPrefix(tuple.Tuple{keyOpsOutbox}.Pack())
}
