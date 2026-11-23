package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) resolveDirectReference(
	ctx context.Context,
	nt *node.NodeType,
	input string,
) (uuid.UUID, error) {
	propName := strings.TrimSpace(nt.Reference.Property)
	if propName == "" {
		return uuid.Nil, fmt.Errorf(
			"%s reference property is empty: %w",
			nt.Slug,
			domain.ErrInvalidArgument,
		)
	}

	workspaces, err := r.referenceSearchRoots(ctx)
	if err != nil {
		return uuid.Nil, err
	}

	matches := map[uuid.UUID]struct{}{}
	matchedWorkspaces := map[uuid.UUID]*node.NodeView{}

	for _, workspace := range workspaces {
		for _, rawValue := range referenceLookupValues(input) {
			children, err := r.nodes.ListByProperty(
				ctx,
				workspace.OrgID,
				nt.TypeKey,
				propName,
				rawValue,
			)
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

func (r *Resolver) resolveScopedNodeReference(
	ctx context.Context,
	nt *node.NodeType,
	input string,
) (uuid.UUID, error) {
	propName := strings.TrimSpace(nt.Reference.Property)
	if propName == "" {
		return uuid.Nil, fmt.Errorf(
			"%s reference property is empty: %w",
			nt.Slug,
			domain.ErrInvalidArgument,
		)
	}

	return r.resolveScopedReference(ctx, nt, propName, input)
}
