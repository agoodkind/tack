package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// RenderSearchText renders source with the node name first, followed by
// included property values in projection order, then by definition ID. Each
// value ends with a newline. It omits undeclared, invalid, and inapplicable
// properties with one diagnostic each. Explicit exclusions do not produce
// diagnostics. It returns an error only for incomplete or duplicate
// definitions, malformed declared rules, a missing node type, or an unknown
// property type without a declaration.
func RenderSearchText(ctx context.Context, source SearchSource) (SearchRender, error) {
	render, err := renderSearchText(source)
	if err != nil {
		wrapped := fmt.Errorf("render search text of node %s: %w", source.NodeID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.content.projection_failed",
			slog.String("err", wrapped.Error()), slog.String("node_id", source.NodeID.String()))
		return SearchRender{}, LoggedProjectionError{Cause: wrapped}
	}
	if len(render.Diagnostics) > 0 {
		telemetry.L(ctx).InfoContext(ctx, "search.content.properties_omitted",
			slog.String("node_id", source.NodeID.String()), slog.Int("diagnostics", len(render.Diagnostics)))
	}
	return render, nil
}

// renderedProperty contains the rendered values of one included property.
type renderedProperty struct {
	order  int
	id     uuid.UUID
	leaves []string
}

func renderSearchText(source SearchSource) (SearchRender, error) {
	if source.NodeType == nil {
		return SearchRender{}, definitionError{message: "the organization does not define the node type"}
	}
	definitions, err := indexSearchDefinitions(source.Definitions)
	if err != nil {
		return SearchRender{}, err
	}
	names := make([]string, 0, len(source.Properties))
	for name := range source.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	render := SearchRender{Text: "", Diagnostics: []PropertyDiagnostic{}}
	rendered := make([]renderedProperty, 0, len(names))
	for _, name := range names {
		definition, declared := definitions[name]
		if !declared {
			render.Diagnostics = append(render.Diagnostics, propertyDiagnostic(source, name, uuid.Nil,
				DiagnosticUndeclared, "the organization does not define this property"))
			continue
		}
		projection, err := EffectiveSearchProjection(definition)
		if err != nil {
			return SearchRender{}, err
		}
		if !projection.Include {
			continue
		}
		leaves, err := extractProjectedLeaves(source.Properties[name], projection.Rule)
		var invalid invalidValueError
		if errors.As(err, &invalid) {
			render.Diagnostics = append(render.Diagnostics, propertyDiagnostic(source, name, definition.ID,
				DiagnosticInvalidValue, invalid.message))
			continue
		}
		if err != nil {
			return SearchRender{}, err
		}
		rendered = append(rendered, renderedProperty{order: projection.Order, id: definition.ID, leaves: leaves})
	}
	render.Text = joinRenderedText(source.Name, rendered)
	return render, nil
}

// indexSearchDefinitions maps each definition name to its definition. It
// rejects nil, unnamed, or unidentified definitions, duplicate IDs or names,
// and malformed declared projections.
func indexSearchDefinitions(definitions []*PropertyDef) (map[string]*PropertyDef, error) {
	seenIDs := make(map[uuid.UUID]struct{}, len(definitions))
	byName := make(map[string]*PropertyDef, len(definitions))
	for _, definition := range definitions {
		if definition == nil || definition.ID == uuid.Nil || definition.Name == "" {
			return nil, definitionError{message: "a property definition is incomplete"}
		}
		if _, exists := seenIDs[definition.ID]; exists {
			return nil, definitionError{message: fmt.Sprintf("property definition %s repeats a definition ID", definition.ID)}
		}
		seenIDs[definition.ID] = struct{}{}
		if _, exists := byName[definition.Name]; exists {
			return nil, definitionError{message: fmt.Sprintf("property name %q matches two definitions", definition.Name)}
		}
		byName[definition.Name] = definition
		if definition.Search == nil {
			continue
		}
		if definition.Search.Order < 0 {
			return nil, definitionError{message: fmt.Sprintf("property definition %s has a negative search order", definition.ID)}
		}
		if err := definition.Search.Rule.validate(0); err != nil {
			return nil, definitionError{message: fmt.Sprintf("property definition %s: %s", definition.ID, err.Error())}
		}
	}
	return byName, nil
}

func propertyDiagnostic(source SearchSource, name string, definitionID uuid.UUID, reason DiagnosticReason, message string) PropertyDiagnostic {
	return PropertyDiagnostic{
		OrgID: source.OrgID, NodeID: source.NodeID, PropertyName: name, PropertyDefID: definitionID,
		Revision: "", ProjectionVersion: "", Reason: reason, Message: message,
	}
}

// joinRenderedText writes the node name, then property values in projection
// order, then by definition ID. Each value ends with a newline.
func joinRenderedText(name string, rendered []renderedProperty) string {
	sort.Slice(rendered, func(i, j int) bool {
		if rendered[i].order != rendered[j].order {
			return rendered[i].order < rendered[j].order
		}
		return rendered[i].id.String() < rendered[j].id.String()
	})
	var text strings.Builder
	text.WriteString(name)
	text.WriteByte('\n')
	for _, property := range rendered {
		for _, leaf := range property.leaves {
			text.WriteString(leaf)
			text.WriteByte('\n')
		}
	}
	return text.String()
}

// ErrInvalidSearchProjection reports a definition that search cannot render.
var ErrInvalidSearchProjection = errors.New("invalid search projection")

// definitionError wraps ErrInvalidSearchProjection for metadata that the
// renderer cannot interpret.
type definitionError struct{ message string }

func (e definitionError) Error() string { return e.message + ": " + ErrInvalidSearchProjection.Error() }

func (e definitionError) Unwrap() error { return ErrInvalidSearchProjection }
