package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func resolveParentReference(
	ctx context.Context,
	b NodeTypeBinding,
	childType *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	ref string,
) (uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		if err := verifyParentCandidate(ctx, b, orgID, scopeID, id); err != nil {
			return uuid.Nil, err
		}
		return id, nil
	}
	matches := map[uuid.UUID]struct{}{}
	for _, parentType := range parentTypesFor(b.Resolver.typeIndex, childType) {
		id, err := b.Resolver.ResolveTypedNodeID(ctx, parentType, ref)
		if err != nil {
			continue
		}
		if err := verifyParentCandidate(ctx, b, orgID, scopeID, id); err != nil {
			continue
		}
		matches[id] = struct{}{}
	}
	id, err := uniqueMatch(matches, "properties.parent_id", ref)
	if err != nil {
		return uuid.Nil, fmt.Errorf("properties.parent_id %q: %w", ref, domain.ErrInvalidArgument)
	}
	return id, nil
}

func parentTypesFor(typeIndex map[string]*node.NodeType, childType *node.NodeType) []*node.NodeType {
	parents := make([]*node.NodeType, 0)
	seen := map[string]struct{}{}
	for _, typeKey := range childType.CanLiveUnder {
		if parentType := typeIndex[typeKey]; parentType != nil {
			parents = appendParentType(parents, seen, parentType)
		}
	}
	for _, parentType := range typeIndex {
		for _, childKey := range parentType.CanContain {
			if childKey == childType.TypeKey {
				parents = appendParentType(parents, seen, parentType)
				break
			}
		}
	}
	return parents
}

func appendParentType(types []*node.NodeType, seen map[string]struct{}, parentType *node.NodeType) []*node.NodeType {
	key := parentType.TypeKey
	if key == "" {
		key = parentType.Slug
	}
	if key == "" {
		return types
	}
	if _, ok := seen[key]; ok {
		return types
	}
	seen[key] = struct{}{}
	return append(types, parentType)
}

func verifyParentCandidate(ctx context.Context, b NodeTypeBinding, orgID, scopeID, id uuid.UUID) error {
	resolve, err := b.Reader.Resolve(ctx, id)
	if err != nil {
		return err
	}
	if resolve == nil || resolve.OrgID != orgID {
		return domain.ErrNotFound
	}
	if scopeID == uuid.Nil || id == scopeID {
		return nil
	}
	view, err := b.Reader.Get(ctx, id)
	if err != nil {
		return err
	}
	if viewBelongsToScope(ctx, b.Resolver, view, scopeID) {
		return nil
	}
	return domain.ErrNotFound
}
