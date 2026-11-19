package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func normalizePropertyCommandReferences(
	ctx context.Context,
	binding NodeTypeBinding,
	nodeType *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if props == nil {
		return nil, nil
	}

	defs, err := binding.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, loggedNormalizeError(ctx, nodeType, "filter", err)
	}

	compiled, err := service.CompileUpdatePropertyCommand(
		ctx,
		defs,
		binding.Resolver.typeIndex,
		nodeType,
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
			return resolveReferenceProp(ctx, binding.Resolver, orgID, scopeID, targetType, ref)
		},
		func(
			ctx context.Context,
			childType *node.NodeType,
			orgID uuid.UUID,
			scopeID uuid.UUID,
			ref string,
		) (uuid.UUID, error) {
			return resolveParentReference(ctx, binding, childType, orgID, scopeID, ref)
		},
	)
	if err != nil {
		return nil, loggedNormalizeError(ctx, nodeType, "filter", err)
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
	if targetType.Reference.Strategy == node.ReferenceScopedProperty &&
		!strings.Contains(ref, scopedNodeRefSeparator) &&
		scopeID != uuid.Nil {
		propName := strings.TrimSpace(targetType.Reference.Property)
		if propName == "" {
			return uuid.Nil, domain.ErrInvalidArgument
		}

		return resolver.resolveNodeWithinScope(
			ctx,
			orgID,
			scopeID,
			targetType.TypeKey,
			propName,
			ref,
		)
	}

	nodeID, err := resolver.ResolveTypedNodeID(ctx, targetType, ref)
	if err != nil {
		return uuid.Nil, err
	}

	resolve, err := resolver.reader.Resolve(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(ctx, "resolver.reference_read_failed", slog.String("err", err.Error()))

		return uuid.Nil, fmt.Errorf("resolve reference node %s: %w", nodeID, err)
	}

	if resolve == nil || resolve.OrgID != orgID {
		return uuid.Nil, domain.ErrNotFound
	}

	return nodeID, nil
}
