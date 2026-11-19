package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

const nullPropertyValue = "null"

func (s *NodeService) readSchema(ctx context.Context, orgID uuid.UUID) (node.Schema, error) {
	definitions, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list property definitions of organization %s: %w", orgID, err)
		slog.ErrorContext(
			ctx,
			"node.schema_read_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)

		return node.Schema{}, wrapped
	}

	types, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types of organization %s: %w", orgID, err)
		slog.ErrorContext(
			ctx,
			"node.schema_read_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)

		return node.Schema{}, wrapped
	}

	return node.Schema{
		OrgID:       orgID,
		Types:       node.BuildTypeIndex(types),
		Definitions: definitions,
	}, nil
}

func loggedPropertyCheckError(
	ctx context.Context,
	nodeType *node.NodeType,
	operation string,
	err error,
) error {
	wrapped := fmt.Errorf("node type %q %s: %w", nodeType.TypeKey, operation, err)
	if errors.Is(err, domain.ErrInvalidArgument) {
		slog.WarnContext(
			ctx,
			"node.props_refused",
			slog.String("node_type", nodeType.TypeKey),
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	slog.ErrorContext(
		ctx,
		"node.props_check_failed",
		slog.String("node_type", nodeType.TypeKey),
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}

func setUpdateProps(props map[string]json.RawMessage) map[string]json.RawMessage {
	setProps := make(map[string]json.RawMessage, len(props))
	for name, raw := range props {
		if isDeletedPropValue(raw) {
			continue
		}

		setProps[name] = raw
	}

	return setProps
}

func isDeletedPropValue(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == nullPropertyValue
}
