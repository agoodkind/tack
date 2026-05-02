package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) resolveScopeReference(ctx context.Context, parent *node.NodeView, level ScopeLevel, ref string) (*node.NodeView, error) {
	nt := r.typeIndex[level.TypeKey]
	if nt == nil {
		return nil, fmt.Errorf("node type %q: %w", level.TypeKey, domain.ErrNotFound)
	}
	id, err := r.resolveNodeUnderParent(ctx, parent, nt, ref)
	if err != nil {
		return nil, err
	}
	view, err := r.reader.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	return view, nil
}

func (r *Resolver) resolveNodeUnderParent(ctx context.Context, parent *node.NodeView, nt *node.NodeType, ref string) (uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		view, err := r.reader.Get(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if view != nil && view.NodeType == nt.TypeKey && viewBelongsToScope(ctx, r, view, parent.ID) {
			return id, nil
		}
		return uuid.Nil, domain.ErrNotFound
	}
	switch nt.Reference.Strategy {
	case node.ReferenceDirectSlug:
		return r.resolveDirectNodeUnderParent(ctx, parent, nt, ref)
	case node.ReferenceScopedSequence:
		id, err := r.ResolveTypedNodeID(ctx, nt, ref)
		if err != nil {
			return uuid.Nil, err
		}
		view, err := r.reader.Get(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if viewBelongsToScope(ctx, r, view, parent.ID) {
			return id, nil
		}
		return uuid.Nil, domain.ErrNotFound
	case node.ReferenceScopedProperty:
		if strings.Contains(ref, scopedNodeRefSeparator) {
			id, err := r.ResolveTypedNodeID(ctx, nt, ref)
			if err != nil {
				return uuid.Nil, err
			}
			view, err := r.reader.Get(ctx, id)
			if err != nil {
				return uuid.Nil, err
			}
			if viewBelongsToScope(ctx, r, view, parent.ID) {
				return id, nil
			}
			return uuid.Nil, domain.ErrNotFound
		}
		propName := strings.TrimSpace(nt.Reference.Property)
		if propName == "" {
			return uuid.Nil, domain.ErrInvalidArgument
		}
		return r.resolveNodeWithinScope(ctx, parent.OrgID, parent.ID, nt.TypeKey, propName, ref)
	default:
		return uuid.Nil, invalidTypedNodeIDError(nt, ref)
	}
}

func (r *Resolver) resolveDirectNodeUnderParent(ctx context.Context, parent *node.NodeView, nt *node.NodeType, ref string) (uuid.UUID, error) {
	propName := strings.TrimSpace(nt.Reference.Property)
	if propName == "" {
		return uuid.Nil, domain.ErrInvalidArgument
	}
	matches := map[uuid.UUID]struct{}{}
	for _, rawValue := range referenceLookupValues(ref) {
		children, err := r.nodes.ListByProperty(ctx, parent.OrgID, nt.TypeKey, propName, rawValue)
		if err != nil {
			continue
		}
		for _, child := range children {
			if r.nodeBelongsToScope(ctx, child, parent.ID) {
				matches[child.ID] = struct{}{}
			}
		}
	}
	return uniqueMatch(matches, strings.ToLower(nt.Slug), ref)
}
