package node

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReferencePartKind selects the component kind within a reference key
// template.
type ReferencePartKind string

const (
	// ReferencePartScopeRef renders the reference of the nearest ancestor that
	// declares the feature in ReferencePart.Value.
	ReferencePartScopeRef ReferencePartKind = "scope_ref"
	// ReferencePartProperty renders the node property in ReferencePart.Value.
	ReferencePartProperty ReferencePartKind = "property"
	// ReferencePartNodeType renders the node's TypeKey and ignores Value.
	ReferencePartNodeType ReferencePartKind = "node_type"
	// ReferencePartLiteral renders ReferencePart.Value verbatim.
	ReferencePartLiteral ReferencePartKind = "literal"
)

// ReferencePart defines one component of a reference key template.
type ReferencePart struct {
	Kind ReferencePartKind `json:"kind"`
	// Value contains the feature name, property name, or literal text that Kind
	// requires. ReferencePartNodeType ignores Value.
	Value string `json:"value,omitempty"`
}

// ReferenceTemplate requires its Parts to render a unique string. Two nodes
// in one organization must not render the same string for the same template.
// The template with IsPrimary set also renders the human-readable reference
// for its NodeType.
type ReferenceTemplate struct {
	// Name identifies the template within its NodeType and appears in the
	// storage key and in conflict errors.
	Name string `json:"name"`
	// Parts render in order.
	Parts []ReferencePart `json:"parts"`
	// IsPrimary selects the template that renders the human-readable reference.
	// At most one template per NodeType sets IsPrimary.
	IsPrimary bool `json:"is_primary,omitempty"`
	// Generated selects the property that a value generator fills during node
	// creation. An empty value requires the caller to supply every part.
	Generated string `json:"generated,omitempty"`
}

// ReferenceRenderInput supplies every value that a template needs to render.
type ReferenceRenderInput struct {
	// NodeTypeKey contains the current node's type key.
	NodeTypeKey string
	// Props maps property names to their JSON values.
	Props map[string]json.RawMessage
	// ScopeRefs maps a feature name to its nearest ancestor's rendered reference.
	ScopeRefs map[string]string
}

// Render produces the template's string from its parts in order. It rejects
// a missing required value instead of rendering an incomplete reference that
// could conflict with another reference.
//
// Errors include the template and part context as text. The service boundary
// logs the error once with the request context.
//
//   - Literal, node type, scope reference, and property parts supply segments.
func (t ReferenceTemplate) Render(in ReferenceRenderInput) (string, error) {
	var output strings.Builder
	for _, part := range t.Parts {
		segment, err := renderReferencePart(part, in)
		if err != nil {
			return "", fmt.Errorf("render template %q: %s", t.Name, err.Error())
		}
		output.WriteString(segment)
	}
	return output.String(), nil
}

// CounterKey renders every template part except the generated property.
//
// The function skips a property part when its Value equals Generated and
// renders the remaining parts in order.
//
//   - Literal separators remain in the counter key.
func (t ReferenceTemplate) CounterKey(in ReferenceRenderInput) (string, error) {
	var output strings.Builder
	for _, part := range t.Parts {
		if part.Kind == ReferencePartProperty && part.Value == t.Generated {
			continue
		}
		segment, err := renderReferencePart(part, in)
		if err != nil {
			return "", fmt.Errorf("counter key for template %q: %s", t.Name, err.Error())
		}
		output.WriteString(segment)
	}
	return output.String(), nil
}

func renderReferencePart(part ReferencePart, in ReferenceRenderInput) (string, error) {
	switch part.Kind {
	case ReferencePartLiteral:
		return part.Value, nil
	case ReferencePartNodeType:
		if in.NodeTypeKey == "" {
			return "", fmt.Errorf("node type part: node type key is empty")
		}
		return in.NodeTypeKey, nil
	case ReferencePartScopeRef:
		value := in.ScopeRefs[part.Value]
		if value == "" {
			return "", fmt.Errorf("scope reference part %q: no ancestor declaring that feature has a reference", part.Value)
		}
		return value, nil
	case ReferencePartProperty:
		value, err := referencePropertyString(in.Props[part.Value])
		if err != nil {
			return "", fmt.Errorf("property part %q: %s", part.Value, err.Error())
		}
		return value, nil
	default:
		return "", fmt.Errorf("unknown reference part kind %q", part.Kind)
	}
}

// referencePropertyString renders a JSON property value as reference text.
// It preserves a number's exact JSON digits without converting through a
// floating-point value. This preserves the precision of an int64 sequence.
func referencePropertyString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("property has no value")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if text == "" {
			return "", fmt.Errorf("property is an empty string")
		}
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", fmt.Errorf("property value %s is neither a string nor a number", raw)
}
