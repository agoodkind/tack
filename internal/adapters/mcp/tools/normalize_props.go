package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func propertyDefAppliesTo(def *node.PropertyDef, nt *node.NodeType) bool {
	return service.PropertyDefAppliesToCommand(def, nt)
}

func mustRawString(value string) json.RawMessage {
	return service.MustRawString(value)
}

func normalizeCreateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, uuid.UUID, error) {
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	command, err := service.CompileCreatePropertyCommand(
		ctx,
		defs,
		b.Resolver.typeIndex,
		nt,
		orgID,
		scopeID,
		props,
		func(ctx context.Context, orgID uuid.UUID, scopeID uuid.UUID, targetType *node.NodeType, ref string) (uuid.UUID, error) {
			return resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		},
		func(ctx context.Context, childType *node.NodeType, orgID uuid.UUID, scopeID uuid.UUID, ref string) (uuid.UUID, error) {
			return resolveParentReference(ctx, b, childType, orgID, scopeID, ref)
		},
	)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if command == nil {
		return nil, scopeID, nil
	}
	return command.Props, command.ParentID, nil
}

func normalizeUpdateProps(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	existing *node.NodeView,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if existing == nil {
		return nil, domain.ErrNotFound
	}
	scopeID := uuidProp(existing, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = uuidProp(existing, "parent_id")
	}
	defs, err := b.PropertyDefs.List(ctx, existing.OrgID)
	if err != nil {
		return nil, err
	}
	command, err := service.CompileUpdatePropertyCommand(
		ctx,
		defs,
		b.Resolver.typeIndex,
		nt,
		existing.OrgID,
		scopeID,
		props,
		func(ctx context.Context, orgID uuid.UUID, scopeID uuid.UUID, targetType *node.NodeType, ref string) (uuid.UUID, error) {
			return resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		},
	)
	if err != nil {
		return nil, err
	}
	if command == nil {
		return nil, nil
	}
	return command.Props, nil
}

func normalizePropertyCommandReferences(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if props == nil {
		return nil, nil
	}
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	compiled, err := service.CompileUpdatePropertyCommand(
		ctx,
		defs,
		b.Resolver.typeIndex,
		nt,
		orgID,
		scopeID,
		props,
		func(ctx context.Context, orgID uuid.UUID, scopeID uuid.UUID, targetType *node.NodeType, ref string) (uuid.UUID, error) {
			return resolveReferenceProp(ctx, b.Resolver, orgID, scopeID, targetType, ref)
		},
	)
	if err != nil {
		return nil, err
	}
	if compiled == nil {
		return nil, nil
	}
	return compiled.Props, nil
}

func resolveReferenceProp(
	ctx context.Context,
	resolver *Resolver,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	ref string,
) (uuid.UUID, error) {
	if targetType.Reference.Strategy == node.ReferenceScopedProperty && !strings.Contains(ref, scopedNodeRefSeparator) && scopeID != uuid.Nil {
		propName := strings.TrimSpace(targetType.Reference.Property)
		if propName == "" {
			return uuid.Nil, domain.ErrInvalidArgument
		}
		return resolver.resolveNodeWithinScope(ctx, orgID, scopeID, targetType.TypeKey, propName, ref)
	}
	id, err := resolver.ResolveTypedNodeID(ctx, targetType, ref)
	if err != nil {
		return uuid.Nil, err
	}
	resolve, err := resolver.reader.Resolve(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	if resolve == nil || resolve.OrgID != orgID {
		return uuid.Nil, domain.ErrNotFound
	}
	return id, nil
}
