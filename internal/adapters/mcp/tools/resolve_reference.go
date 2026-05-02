package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) resolveDirectReference(ctx context.Context, nt *node.NodeType, input string) (uuid.UUID, error) {
	propName := strings.TrimSpace(nt.Reference.Property)
	if propName == "" {
		return uuid.Nil, fmt.Errorf("%s reference property is empty: %w", nt.Slug, domain.ErrInvalidArgument)
	}
	workspaces, err := r.referenceSearchRoots(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	matches := map[uuid.UUID]struct{}{}
	matchedWorkspaces := map[uuid.UUID]*node.NodeView{}
	for _, workspace := range workspaces {
		for _, rawValue := range referenceLookupValues(input) {
			children, err := r.nodes.ListByProperty(ctx, workspace.OrgID, nt.TypeKey, propName, rawValue)
			if err != nil {
				continue
			}
			for _, child := range children {
				if !r.nodeBelongsToScope(ctx, child, workspace.ID) {
					continue
				}
				matches[child.ID] = struct{}{}
				matchedWorkspaces[child.ID] = workspace
			}
		}
	}
	id, err := uniqueMatch(matches, strings.ToLower(nt.Slug), input)
	if err != nil {
		return uuid.Nil, err
	}
	stampAuditEntryPoint(ctx, matchedWorkspaces[id])
	return id, nil
}

func (r *Resolver) resolveScopedNodeReference(ctx context.Context, nt *node.NodeType, input string) (uuid.UUID, error) {
	scopeIdent, localRef, ok := strings.Cut(input, scopedNodeRefSeparator)
	if !ok {
		return uuid.Nil, invalidTypedNodeIDError(nt, input)
	}
	scopeIdent = strings.TrimSpace(scopeIdent)
	localRef = strings.TrimSpace(localRef)
	if scopeIdent == "" || localRef == "" {
		return uuid.Nil, invalidTypedNodeIDError(nt, input)
	}
	propName := strings.TrimSpace(nt.Reference.Property)
	if propName == "" {
		return uuid.Nil, fmt.Errorf("%s reference property is empty: %w", nt.Slug, domain.ErrInvalidArgument)
	}
	level, ok := r.referenceScopeLevel(nt)
	if !ok {
		return uuid.Nil, fmt.Errorf("%s %q: %w", strings.ToLower(nt.Slug), input, domain.ErrNotFound)
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	workspaces, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	matches := map[uuid.UUID]struct{}{}
	matchedWorkspaces := map[uuid.UUID]*node.NodeView{}
	for _, workspace := range workspaces {
		scopeNode, err := r.ResolveScope(ctx, workspace, level, scopeIdent)
		if err != nil {
			continue
		}
		id, err := r.resolveNodeWithinScope(ctx, workspace.OrgID, scopeNode.ID, nt.TypeKey, propName, localRef)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidArgument) {
				return uuid.Nil, err
			}
			continue
		}
		matches[id] = struct{}{}
		matchedWorkspaces[id] = workspace
	}
	id, err := uniqueMatch(matches, strings.ToLower(nt.Slug), input)
	if err != nil {
		return uuid.Nil, err
	}
	stampAuditEntryPoint(ctx, matchedWorkspaces[id])
	return id, nil
}

func (r *Resolver) resolveNodeWithinScope(ctx context.Context, orgID, scopeID uuid.UUID, typeKey, propName, localRef string) (uuid.UUID, error) {
	scopeIDRaw, _ := json.Marshal(scopeID.String())
	views, err := r.reader.List(ctx, node.NodeListQuery{
		OrgID:    orgID,
		NodeType: typeKey,
		ByProperty: &node.PropertyMatch{
			PropName: "parent_id",
			Value:    scopeIDRaw,
		},
	})
	if err != nil {
		return uuid.Nil, err
	}
	matches := map[uuid.UUID]struct{}{}
	for _, view := range views {
		if matchesReferenceProperty(view, propName, localRef) {
			matches[view.ID] = struct{}{}
		}
	}
	return uniqueMatch(matches, typeKey, localRef)
}
