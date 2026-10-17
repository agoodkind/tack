package node

import (
	"encoding/json"

	"github.com/google/uuid"
)

// DiagnosticReason is the stable code for one property diagnostic.
type DiagnosticReason string

const (
	// DiagnosticUndeclared reports a property without a definition.
	DiagnosticUndeclared DiagnosticReason = "undeclared_property"
	// DiagnosticInapplicable reports a definition that does not apply to the
	// node type.
	DiagnosticInapplicable DiagnosticReason = "inapplicable_property"
	// DiagnosticInvalidValue reports a value that violates its definition or
	// text rule.
	DiagnosticInvalidValue DiagnosticReason = "invalid_value"
)

// PropertyDiagnostic reports one property that search text omits. It does
// not contain the source value.
type PropertyDiagnostic struct {
	OrgID        uuid.UUID `json:"org_id"`
	NodeID       uuid.UUID `json:"node_id"`
	PropertyName string    `json:"property_name"`
	// PropertyDefID is uuid.Nil for an undeclared property.
	PropertyDefID     uuid.UUID        `json:"property_def_id"`
	Revision          string           `json:"revision"`
	ProjectionVersion string           `json:"projection_version"`
	Reason            DiagnosticReason `json:"reason"`
	Message           string           `json:"message"`
}

// SearchSource contains the committed node state for rendering search text.
type SearchSource struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	NodeType   *NodeType
	Name       string
	Properties map[string]json.RawMessage
	// Definitions contains the organization definitions that match the
	// node's property names.
	Definitions []*PropertyDef
}

// SearchRender contains one node's search text and property diagnostics.
// The diagnostics sort by property name.
type SearchRender struct {
	Text        string
	Diagnostics []PropertyDiagnostic
}
