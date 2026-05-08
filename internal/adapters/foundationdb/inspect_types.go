package foundationdb

import (
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const legacySlugIndexKeyFamily = "slug_index"

// InspectStore exposes read-only record-family inspection helpers for operator
// repair tooling.
type InspectStore struct {
	db fdb.Database
}

// NodeInspectionReport is the raw, family-oriented snapshot for one node UUID.
type NodeInspectionReport struct {
	NodeID                  uuid.UUID                       `json:"node_id"`
	Resolve                 *node.NodeResolve               `json:"resolve,omitempty"`
	NodeInstances           []InspectNodeInstance           `json:"node_instances,omitempty"`
	NodeViews               []InspectNodeView               `json:"node_views,omitempty"`
	PropertyIndexRows       []InspectPropertyIndexRow       `json:"property_index_rows,omitempty"`
	LegacyAddressRows       []InspectLegacyAddressRow       `json:"legacy_address_rows,omitempty"`
	Relationships           []InspectRelationshipRow        `json:"relationships,omitempty"`
	RelationshipReverseRows []InspectRelationshipReverseRow `json:"relationship_reverse_rows,omitempty"`
}

// InspectNodeInstance is one node_instance row that matched the inspected UUID.
type InspectNodeInstance struct {
	OrgID    uuid.UUID  `json:"org_id"`
	NodeType string     `json:"node_type"`
	NodeID   uuid.UUID  `json:"node_id"`
	Value    *node.Node `json:"value,omitempty"`
}

// InspectNodeView is one node_view row that matched the inspected UUID.
type InspectNodeView struct {
	OrgID    uuid.UUID      `json:"org_id"`
	NodeType string         `json:"node_type"`
	NodeID   uuid.UUID      `json:"node_id"`
	Value    *node.NodeView `json:"value,omitempty"`
}

// InspectPropertyIndexRow is one node_by_property row that matched the UUID.
type InspectPropertyIndexRow struct {
	OrgID        uuid.UUID       `json:"org_id"`
	NodeType     string          `json:"node_type"`
	PropertyName string          `json:"property_name"`
	EncodedValue json.RawMessage `json:"encoded_value,omitempty"`
	NodeID       uuid.UUID       `json:"node_id"`
}

// InspectLegacyAddressRow is one legacy address row that points at the inspected UUID.
type InspectLegacyAddressRow struct {
	LegacyKeyFamily string    `json:"legacy_key_family"`
	NodeType        string    `json:"node_type"`
	AddressKind     string    `json:"address_kind"`
	AddressValue    string    `json:"address_value"`
	OwnerID         uuid.UUID `json:"owner_id"`
}

// InspectRelationshipRow is one forward relationship row where the UUID is the
// source node.
type InspectRelationshipRow struct {
	OrgID             uuid.UUID          `json:"org_id"`
	SourceID          uuid.UUID          `json:"source_id"`
	RelationType      string             `json:"relation_type"`
	TargetID          uuid.UUID          `json:"target_id"`
	ReverseRowPresent bool               `json:"reverse_row_present"`
	Value             *node.Relationship `json:"value,omitempty"`
}

// InspectRelationshipReverseRow is one reverse relationship row where the UUID
// is the target node.
type InspectRelationshipReverseRow struct {
	OrgID             uuid.UUID `json:"org_id"`
	TargetID          uuid.UUID `json:"target_id"`
	RelationType      string    `json:"relation_type"`
	SourceID          uuid.UUID `json:"source_id"`
	ForwardRowPresent bool      `json:"forward_row_present"`
}
