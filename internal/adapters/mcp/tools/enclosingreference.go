package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

var errScopedUnauthenticated = errors.New("unauthenticated")

// resolveScopedReference resolves "<scope reference>::<value>" to the one node
// of type nt under that scope with propName equal to value. The scope is the
// first level of the scope chain of nt, or the entry point when nt lives
// directly under it.
func (r *Resolver) resolveScopedReference(
	ctx context.Context,
	nodeType *node.NodeType,
	propName string,
	input string,
) (uuid.UUID, error) {
	scopeIdent, localRef, found := strings.Cut(input, scopedNodeRefSeparator)
	if !found {
		return uuid.Nil, invalidTypedNodeIDError(nodeType, input)
	}

	scopeIdent = strings.TrimSpace(scopeIdent)
	localRef = strings.TrimSpace(localRef)
	if scopeIdent == "" || localRef == "" {
		return uuid.Nil, invalidTypedNodeIDError(nodeType, input)
	}

	level, found := r.referenceScopeLevel(nodeType)
	if !found && r.livesUnderEntryPoint(nodeType) {
		return r.resolveEntryPointScopedReference(ctx, nodeType, propName, scopeIdent, localRef)
	}

	if !found {
		err := fmt.Errorf("%s %q: %w", strings.ToLower(nodeType.Slug), input, domain.ErrNotFound)
		slog.WarnContext(
			ctx,
			"resolver.scoped_reference.no_scope",
			slog.String("node_type", nodeType.TypeKey),
			slog.String("err", err.Error()),
		)

		return uuid.Nil, err
	}

	userID, found := auth.UserID(ctx)
	if !found {
		return uuid.Nil, errScopedUnauthenticated
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

		nodeID, err := r.resolveNodeWithinScope(
			ctx,
			workspace.OrgID,
			scopeNode.ID,
			nodeType.TypeKey,
			propName,
			localRef,
		)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidArgument) {
				return uuid.Nil, err
			}

			continue
		}

		matches[nodeID] = struct{}{}
		matchedWorkspaces[nodeID] = workspace
	}

	nodeID, err := uniqueMatch(matches, strings.ToLower(nodeType.Slug), input)
	if err != nil {
		return uuid.Nil, err
	}

	stampAuditEntryPoint(ctx, matchedWorkspaces[nodeID])

	return nodeID, nil
}

func (r *Resolver) resolveNodeWithinScope(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	typeKey string,
	propName string,
	localRef string,
) (uuid.UUID, error) {
	views, err := r.reader.List(
		ctx,
		node.NodeListQuery{
			OrgID:            orgID,
			NodeType:         typeKey,
			ByProperty:       nil,
			BySourceRelation: nil,
			ByTargetRelation: &node.RelationScan{
				NodeID:       scopeID,
				RelationType: node.RelChildOf,
			},
			CreatedAfter:  nil,
			CreatedBefore: nil,
			PropFilters:   nil,
			Limit:         0,
			Cursor:        "",
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "resolver.scope.list_failed", slog.String("err", err.Error()))

		return uuid.Nil, fmt.Errorf("list %s nodes under scope %s: %w", typeKey, scopeID, err)
	}

	matches := map[uuid.UUID]struct{}{}

	for _, view := range views {
		if matchesReferenceProperty(view, propName, localRef) {
			matches[view.ID] = struct{}{}
		}
	}

	return uniqueMatch(matches, typeKey, localRef)
}
