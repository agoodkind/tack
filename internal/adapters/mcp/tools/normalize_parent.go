package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

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
	parentTypes := parentTypesFor(b.Resolver.typeIndex, childType)
	if id, err := uuid.Parse(ref); err == nil {
		err := verifyParentCandidate(ctx, b, parentTypes, orgID, scopeID, id)
		if errors.Is(err, errParentTypeRefused) {
			return uuid.Nil, refusedParentReference(ctx, childType, ref)
		}
		if err != nil {
			return uuid.Nil, err
		}
		return id, nil
	}
	matches := map[uuid.UUID]struct{}{}
	for _, parentType := range parentTypes {
		id, err := b.Resolver.ResolveTypedNodeID(ctx, parentType, ref)
		if err != nil {
			continue
		}
		if err := verifyParentCandidate(ctx, b, parentTypes, orgID, scopeID, id); err != nil {
			continue
		}
		matches[id] = struct{}{}
	}
	id, err := uniqueMatch(matches, "properties.parent_id", ref)
	if err != nil {
		return uuid.Nil, refusedParentReference(ctx, childType, ref)
	}
	return id, nil
}

// errParentTypeRefused reports a parent candidate with a node type that is
// not in the parent types of the child type.
var errParentTypeRefused = errors.New("the parent node type is not a parent type of the child type")

// refusedParentReference logs and returns the error for a parent_id
// reference that does not name exactly one permitted parent of childType. It
// wraps domain.ErrInvalidArgument.
func refusedParentReference(ctx context.Context, childType *node.NodeType, ref string) error {
	err := fmt.Errorf("properties.parent_id %q: %w", ref, domain.ErrInvalidArgument)
	slog.WarnContext(ctx, "node.parent_reference_refused", slog.String("node_type", childType.TypeKey), slog.String("err", err.Error()))
	return err
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

// verifyParentCandidate checks that id resolves in orgID, that its node type
// is one of parentTypes, and that it belongs to scopeID. It returns
// errParentTypeRefused for a node type outside parentTypes.
func verifyParentCandidate(ctx context.Context, b NodeTypeBinding, parentTypes []*node.NodeType, orgID, scopeID, id uuid.UUID) error {
	resolve, err := b.Reader.Resolve(ctx, id)
	if err != nil {
		return err
	}
	if resolve == nil || resolve.OrgID != orgID {
		return domain.ErrNotFound
	}
	if !slices.ContainsFunc(parentTypes, func(parentType *node.NodeType) bool { return parentType.TypeKey == resolve.NodeType }) {
		return errParentTypeRefused
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
