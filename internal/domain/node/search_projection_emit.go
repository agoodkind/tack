package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// EmitSearchText builds deterministic text from the stored projection declarations.
func EmitSearchText(
	ctx context.Context,
	nodeID uuid.UUID,
	name string,
	properties map[string]json.RawMessage,
	definitions []*PropertyDef,
) (string, error) {
	text, err := emitSearchText(ctx, nodeID, name, properties, definitions)
	if err != nil {
		if IsLoggedProjectionError(err) {
			return "", err
		}
		wrapped := fmt.Errorf("emit projected search text: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.content.projection_failed",
			slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
		return "", LoggedProjectionError{Cause: wrapped}
	}
	return text, nil
}

func emitSearchText(
	ctx context.Context,
	nodeID uuid.UUID,
	name string,
	properties map[string]json.RawMessage,
	definitions []*PropertyDef,
) (string, error) {
	seenIDs := make(map[uuid.UUID]struct{}, len(definitions))
	seenNames := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition == nil || definition.ID == uuid.Nil || definition.Name == "" {
			return "", ErrInvalidSearchProjection
		}
		if definition.Search == nil {
			wrapped := fmt.Errorf("emit projected search text for node %s: property definition %s has no search declaration: %w",
				nodeID, definition.ID, ErrInvalidSearchProjection)
			telemetry.L(ctx).ErrorContext(ctx, "search.content.projection_failed",
				slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()),
				slog.String("property_def_id", definition.ID.String()))
			return "", LoggedProjectionError{Cause: wrapped}
		}
		if _, exists := seenIDs[definition.ID]; exists {
			return "", ErrInvalidSearchProjection
		}
		seenIDs[definition.ID] = struct{}{}
		if _, exists := seenNames[definition.Name]; exists {
			return "", ErrInvalidSearchProjection
		}
		seenNames[definition.Name] = struct{}{}
		if definition.Search.Order < 0 {
			return "", ErrInvalidSearchProjection
		}
		if err := definition.Search.Rule.validate(0); err != nil {
			return "", err
		}
	}
	ordered := orderedSearchDefinitions(definitions)
	knownNames := make(map[string]struct{}, len(ordered))
	for _, definition := range ordered {
		knownNames[definition.Name] = struct{}{}
	}
	if undeclared := undeclaredPropertyNames(properties, knownNames); len(undeclared) > 0 {
		names := strings.Join(undeclared, ", ")
		wrapped := fmt.Errorf("emit projected search text for node %s: properties %s have no search declaration: %w",
			nodeID, names, ErrInvalidSearchProjection)
		telemetry.L(ctx).ErrorContext(ctx, "search.content.projection_failed",
			slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()), slog.String("properties", names))
		return "", LoggedProjectionError{Cause: wrapped}
	}
	var text strings.Builder
	text.WriteString(name)
	text.WriteByte('\n')
	for _, definition := range ordered {
		if !definition.Search.Include {
			continue
		}
		raw, exists := properties[definition.Name]
		if !exists {
			continue
		}
		leaves, extractErr := extractProjectedLeaves(ctx, raw, definition.Search.Rule)
		if extractErr != nil {
			return "", extractErr
		}
		for _, leaf := range leaves {
			text.WriteString(leaf)
			text.WriteByte('\n')
		}
	}
	return text.String(), nil
}

// ErrInvalidSearchProjection reports an invalid text declaration or value.
var ErrInvalidSearchProjection = errors.New("invalid search projection")

// undeclaredPropertyNames returns the sorted, quoted names of the properties
// that no definition declares.
func undeclaredPropertyNames(properties map[string]json.RawMessage, knownNames map[string]struct{}) []string {
	names := make([]string, 0)
	for propertyName := range properties {
		if _, exists := knownNames[propertyName]; !exists {
			names = append(names, propertyName)
		}
	}
	sort.Strings(names)
	quoted := make([]string, 0, len(names))
	for _, propertyName := range names {
		quoted = append(quoted, strconv.Quote(propertyName))
	}
	return quoted
}

func orderedSearchDefinitions(definitions []*PropertyDef) []*PropertyDef {
	ordered := append([]*PropertyDef(nil), definitions...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Search.Order != ordered[j].Search.Order {
			return ordered[i].Search.Order < ordered[j].Search.Order
		}
		return ordered[i].ID.String() < ordered[j].ID.String()
	})
	return ordered
}
