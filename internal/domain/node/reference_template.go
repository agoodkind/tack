package node

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReferencePartKind names one component of a reference key template.
type ReferencePartKind string

const (
	// ReferencePartScopeRef renders the rendered reference of the nearest
	// ancestor declaring the feature named in ReferencePart.Value.
	ReferencePartScopeRef ReferencePartKind = "scope_ref"
	// ReferencePartProperty renders the node property named in ReferencePart.Value.
	ReferencePartProperty ReferencePartKind = "property"
	// ReferencePartNodeType renders the node's own TypeKey. Value is unused.
	ReferencePartNodeType ReferencePartKind = "node_type"
	// ReferencePartLiteral renders ReferencePart.Value verbatim.
	ReferencePartLiteral ReferencePartKind = "literal"
)

// ReferencePart is one component of a reference key template.
type ReferencePart struct {
	Kind ReferencePartKind `json:"kind"`
	// Value carries the feature name, property name, or literal text the Kind
	// requires. ReferencePartNodeType ignores it.
	Value string `json:"value,omitempty"`
}

// ReferenceTemplate declares a uniqueness constraint over the string its Parts
// render. Two nodes in one org may not render the same string for the same
// template. The template marked IsPrimary is also the human-readable reference
// for its NodeType.
type ReferenceTemplate struct {
	// Name identifies the template within its NodeType and appears in the
	// storage key and in conflict errors.
	Name string `json:"name"`
	// Parts render in order.
	Parts []ReferencePart `json:"parts"`
	// IsPrimary marks the template that renders the human-readable reference.
	// At most one template per NodeType sets it.
	IsPrimary bool `json:"is_primary,omitempty"`
	// Generated names the property a value generator fills at create time.
	// Empty means the caller supplies every part.
	Generated string `json:"generated,omitempty"`
}

// ReferenceRenderInput carries everything a template needs to render.
type ReferenceRenderInput struct {
	// NodeTypeKey is the current node's type key.
	NodeTypeKey string
	// Props maps property names to their JSON values.
	Props map[string]json.RawMessage
	// ScopeRefs maps a feature name to its nearest ancestor's rendered reference.
	ScopeRefs map[string]string
}

// Render produces the template's string. It fails when a part has no value,
// because a partially rendered reference would silently collide with another.
// Errors carry the template and part context as text and are logged once at
// the service boundary, which holds the request context.
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

// referencePropertyString renders a JSON property value as reference text. A
// number keeps its exact JSON digits, so an int64 sequence never loses
// precision through a float round-trip.
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
