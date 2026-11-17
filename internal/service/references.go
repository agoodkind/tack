package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// updateScopeID reads the scope a node's merged properties place it in. It
// prefers the explicit scope pointer and falls back to the parent, matching
// the
// walk the create path performs.
func updateScopeID(props map[string]json.RawMessage) uuid.UUID {
	scopeID := rawUUIDReferenceProperty(props, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = rawUUIDReferenceProperty(props, "parent_id")
	}

	return scopeID
}

func (s *NodeService) referenceKeysFor(
	ctx context.Context,
	orgID uuid.UUID,
	nodeType *node.NodeType,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) ([]node.ReferenceKey, error) {
	if len(nodeType.ReferenceTemplates) == 0 {
		return nil, nil
	}

	scopeRefs, err := s.scopeRefsFor(ctx, orgID, scopeID)
	if err != nil {
		return nil, err
	}

	keys := make([]node.ReferenceKey, 0, len(nodeType.ReferenceTemplates))

	input := node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       props,
		ScopeRefs:   scopeRefs,
	}
	for _, template := range nodeType.ReferenceTemplates {
		encoded, renderErr := template.Render(input)
		if renderErr != nil {
			slog.ErrorContext(
				ctx,
				"node.reference.render_failed",
				slog.String("err", renderErr.Error()),
			)

			return nil, fmt.Errorf(
				"render reference template %q for node type %q: %w",
				template.Name,
				nodeType.TypeKey,
				renderErr,
			)
		}

		keys = append(keys, node.ReferenceKey{TemplateName: template.Name, Encoded: encoded})
	}

	return keys, nil
}

func rawUUIDReferenceProperty(props map[string]json.RawMessage, propertyName string) uuid.UUID {
	var encoded string
	if err := json.Unmarshal(props[propertyName], &encoded); err != nil {
		return uuid.Nil
	}

	identifier, err := uuid.Parse(encoded)
	if err != nil {
		return uuid.Nil
	}

	return identifier
}
