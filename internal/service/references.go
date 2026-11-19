package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// updateScopeID reads the scope property or uses the requested parent or
// the stored parent.
func updateScopeID(
	props map[string]json.RawMessage,
	parentID uuid.UUID,
	change *node.ParentChange,
) uuid.UUID {
	scopeID := rawUUIDReferenceProperty(props, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = parentID
		if change != nil {
			scopeID = change.TargetID
		}
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
