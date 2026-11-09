package foundationdb

import (
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// InspectStore provides read-only inspection of record families for operator
// repair tools.
type InspectStore struct {
	db fdb.Database
}

// NodeInspectionReport contains the raw records for one node UUID, grouped
// by record family.
type NodeInspectionReport struct {
	NodeID                  uuid.UUID                       `json:"node_id"`
	Resolve                 *node.NodeResolve               `json:"resolve,omitempty"`
	NodeInstances           []InspectNodeInstance           `json:"node_instances,omitempty"`
	NodeViews               []InspectNodeView               `json:"node_views,omitempty"`
	PropertyIndexRows       []InspectPropertyIndexRow       `json:"property_index_rows,omitempty"`
	Relationships           []InspectRelationshipRow        `json:"relationships,omitempty"`
	RelationshipReverseRows []InspectRelationshipReverseRow `json:"relationship_reverse_rows,omitempty"`
}

// InspectNodeInstance contains one node_instance row that matches the
// inspected UUID.
type InspectNodeInstance struct {
	OrgID    uuid.UUID  `json:"org_id"`
	NodeType string     `json:"node_type"`
	NodeID   uuid.UUID  `json:"node_id"`
	Value    *node.Node `json:"value,omitempty"`
}

// InspectNodeView contains one node_view row that matches the inspected
// UUID.
type InspectNodeView struct {
	OrgID    uuid.UUID      `json:"org_id"`
	NodeType string         `json:"node_type"`
	NodeID   uuid.UUID      `json:"node_id"`
	Value    *node.NodeView `json:"value,omitempty"`
}

// InspectPropertyIndexRow contains one node_by_property row that matches the
// UUID.
type InspectPropertyIndexRow struct {
	OrgID        uuid.UUID       `json:"org_id"`
	NodeType     string          `json:"node_type"`
	PropertyName string          `json:"property_name"`
	EncodedValue json.RawMessage `json:"encoded_value,omitempty"`
	NodeID       uuid.UUID       `json:"node_id"`
}

// InspectRelationshipRow contains one forward relationship row with the
// inspected UUID as the source node.
type InspectRelationshipRow struct {
	OrgID             uuid.UUID          `json:"org_id"`
	SourceID          uuid.UUID          `json:"source_id"`
	RelationType      string             `json:"relation_type"`
	TargetID          uuid.UUID          `json:"target_id"`
	ReverseRowPresent bool               `json:"reverse_row_present"`
	Value             *node.Relationship `json:"value,omitempty"`
}

// InspectRelationshipReverseRow contains one reverse relationship row with
// the inspected UUID as the target node.
type InspectRelationshipReverseRow struct {
	OrgID             uuid.UUID `json:"org_id"`
	TargetID          uuid.UUID `json:"target_id"`
	RelationType      string    `json:"relation_type"`
	SourceID          uuid.UUID `json:"source_id"`
	ForwardRowPresent bool      `json:"forward_row_present"`
}
