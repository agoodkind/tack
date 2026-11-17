package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeService) resolveDefaultScopedPropertyReference(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	propName string,
	ref string,
) (uuid.UUID, error) {
	scopeIDRaw := mustRawString(scopeID.String())
	views, err := s.reader.List(
		ctx,
		node.NodeListQuery{
			OrgID:    orgID,
			NodeType: targetType.TypeKey,
			ByProperty: &node.PropertyMatch{
				PropName: propertyParentID,
				Value:    scopeIDRaw,
			},
			BySourceRelation: nil,
			ByTargetRelation: nil,
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"node.default_reference_read_failed",
			slog.String("err", err.Error()),
		)

		return uuid.Nil, fmt.Errorf(
			"list %s children for default reference %q: %w",
			targetType.TypeKey,
			ref,
			err,
		)
	}

	matches := make(map[uuid.UUID]struct{})

	for _, view := range views {
		if matchesDefaultReferenceProperty(view, propName, ref) {
			matches[view.ID] = struct{}{}
		}
	}

	return uniqueDefaultReferenceMatch(matches, targetType.TypeKey, ref)
}

func matchesDefaultReferenceProperty(view *node.NodeView, propName, input string) bool {
	if view == nil {
		return false
	}

	if propName == "name" && strings.EqualFold(view.Name, input) {
		return true
	}

	value := stringPropertyValue(view.Props, propName)

	return value != "" && strings.EqualFold(value, input)
}

func uniqueDefaultReferenceMatch(
	matches map[uuid.UUID]struct{},
	kind string,
	input string,
) (uuid.UUID, error) {
	switch len(matches) {
	case 0:
		slog.Warn(
			"node.default_reference_missing",
			slog.String("kind", kind),
			slog.String("reference", input),
		)

		return uuid.Nil, fmt.Errorf("%s default reference %q: %w", kind, input, domain.ErrNotFound)
	case 1:
		for id := range matches {
			return id, nil
		}
	}

	slog.Warn(
		"node.default_reference_ambiguous",
		slog.String("kind", kind),
		slog.String("reference", input),
	)

	return uuid.Nil, fmt.Errorf(
		"%s default reference %q matched multiple nodes: %w",
		kind,
		input,
		domain.ErrInvalidArgument,
	)
}

func propertyIsPresent(props map[string]json.RawMessage, name string) bool {
	_, ok := props[name]

	return ok
}

func propertyHasValue(props map[string]json.RawMessage, name string) bool {
	raw, ok := props[name]
	if !ok || len(raw) == 0 {
		return false
	}

	return string(raw) != nullPropertyValue
}

func stringPropertyValue(props map[string]json.RawMessage, name string) string {
	raw, ok := props[name]
	if !ok || len(raw) == 0 {
		return ""
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}

	return strings.TrimSpace(value)
}
