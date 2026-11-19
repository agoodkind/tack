package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func mustRawString(value string) json.RawMessage {
	return service.MustRawString(value)
}

// normalizeCreateProps normalizes create properties and resolves the parent ID.
// It also returns the property definitions it loaded.
func normalizeCreateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, uuid.UUID, []*node.PropertyDef, error) {
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, uuid.Nil, nil, err
	}

	command, err := service.CompileCreatePropertyCommand(
		ctx,
		defs,
		b.Resolver.typeIndex,
		nt,
		orgID,
		scopeID,
		props,
		func(
			ctx context.Context,
			orgID uuid.UUID,
			scopeID uuid.UUID,
			targetType *node.NodeType,
			ref string,
		) (uuid.UUID, error) {
			return resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		},
		func(
			ctx context.Context,
			childType *node.NodeType,
			orgID uuid.UUID,
			scopeID uuid.UUID,
			ref string,
		) (uuid.UUID, error) {
			return resolveParentReference(ctx, b, childType, orgID, scopeID, ref)
		},
	)
	if err != nil {
		return nil, uuid.Nil, nil, err
	}

	if command == nil {
		return nil, scopeID, defs, nil
	}

	return command.Props, command.ParentID, defs, nil
}

// normalizeUpdateProps normalizes update properties for the existing node.
// It also returns the property definitions it loaded.
func normalizeUpdateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	existing *node.NodeView,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, []*node.PropertyDef, error) {
	if existing == nil {
		return nil, nil, domain.ErrNotFound
	}

	scopeID := uuidProp(existing, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = uuidProp(existing, "parent_id")
	}

	defs, err := b.PropertyDefs.List(ctx, existing.OrgID)
	if err != nil {
		return nil, nil, err
	}

	command, err := service.CompileUpdatePropertyCommand(
		ctx,
		defs,
		b.Resolver.typeIndex,
		nt,
		existing.OrgID,
		scopeID,
		props,
		func(
			ctx context.Context,
			orgID uuid.UUID,
			scopeID uuid.UUID,
			targetType *node.NodeType,
			ref string,
		) (uuid.UUID, error) {
			return resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		},
		func(
			ctx context.Context,
			childType *node.NodeType,
			orgID uuid.UUID,
			scopeID uuid.UUID,
			ref string,
		) (uuid.UUID, error) {
			return resolveParentReference(ctx, b, childType, orgID, scopeID, ref)
		},
	)
	if err != nil {
		return nil, nil, loggedNormalizeError(ctx, nt, "update", err)
	}

	if command == nil {
		return nil, defs, nil
	}

	return command.Props, defs, nil
}

// loggedNormalizeError logs a refused property command and wraps its cause.
func loggedNormalizeError(ctx context.Context, nt *node.NodeType, command string, err error) error {
	slog.WarnContext(
		ctx,
		"node.props_refused",
		slog.String("node_type", nt.TypeKey),
		slog.String("command", command),
		slog.String("err", err.Error()),
	)

	return fmt.Errorf("normalize %s %s properties: %w", nt.TypeKey, command, err)
}
