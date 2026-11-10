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

// resolveCommandParent reads the parent_id entry of a compiled property
// command and resolves it to a node id. It reports whether the entry was
// present. A present entry must be a non-empty reference string; a null or
// empty value is refused, because a node always has a parent (TACK-523).
func resolveCommandParent(
	ctx context.Context,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	compiled map[string]json.RawMessage,
	resolveParent ParentResolver,
) (uuid.UUID, bool, error) {
	raw, ok := compiled["parent_id"]
	if !ok {
		return uuid.Nil, false, nil
	}
	if isDeletedPropValue(raw) {
		return uuid.Nil, true, invalidParentReference(ctx, nt, "properties.parent_id cannot be empty")
	}
	parentRef, ok := rawString(raw)
	if !ok || strings.TrimSpace(parentRef) == "" {
		return uuid.Nil, true, invalidParentReference(ctx, nt, "properties.parent_id must be a node reference string")
	}
	if resolveParent == nil {
		return uuid.Nil, true, invalidParentReference(ctx, nt, "properties.parent_id has no parent resolver")
	}
	parentID, err := resolveParent(ctx, nt, orgID, scopeID, parentRef)
	if err != nil {
		slog.WarnContext(ctx, "node.parent_reference_unresolved",
			slog.String("node_type", nt.TypeKey), slog.String("reference", parentRef), slog.String("err", err.Error()))
		return uuid.Nil, true, fmt.Errorf("properties.parent_id %q: %w", parentRef, err)
	}
	return parentID, true, nil
}

func invalidParentReference(ctx context.Context, nt *node.NodeType, reason string) error {
	slog.WarnContext(ctx, "node.parent_reference_invalid", slog.String("node_type", nt.TypeKey), slog.String("reason", reason))
	return fmt.Errorf("%s: %w", reason, domain.ErrInvalidArgument)
}
