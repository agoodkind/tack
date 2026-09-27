package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// EmitSearchText builds deterministic text from the stored projection declarations.
func EmitSearchText(
	ctx context.Context,
	name string,
	properties map[string]json.RawMessage,
	definitions []*PropertyDef,
) (string, error) {
	text, err := emitSearchText(ctx, name, properties, definitions)
	if err != nil {
		if IsLoggedProjectionError(err) {
			return "", err
		}
		wrapped := fmt.Errorf("emit projected search text: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.content.projection_failed", slog.String("err", wrapped.Error()))
		return "", LoggedProjectionError{Cause: wrapped}
	}
	return text, nil
}

func emitSearchText(ctx context.Context, name string, properties map[string]json.RawMessage, definitions []*PropertyDef) (string, error) {
	seenIDs := make(map[uuid.UUID]struct{}, len(definitions))
	seenNames := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition == nil || definition.ID == uuid.Nil || definition.Name == "" || definition.Search == nil {
			return "", ErrInvalidSearchProjection
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
	for propertyName := range properties {
		if _, exists := knownNames[propertyName]; !exists {
			return "", ErrInvalidSearchProjection
		}
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
