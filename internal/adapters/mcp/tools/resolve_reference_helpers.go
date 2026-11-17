package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
)

func (r *Resolver) referenceSearchRoots(ctx context.Context) ([]*node.NodeView, error) {
	userID, ok := auth.UserID(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated")
	}

	return r.WorkspacesForUser(ctx, userID)
}

func (r *Resolver) referenceScopeLevel(nt *node.NodeType) (ScopeLevel, bool) {
	chain := r.ScopeChainForType(nt)
	if len(chain) == 0 {
		return ScopeLevel{TypeKey: "", Slug: "", ParamName: ""}, false
	}

	return chain[0], true
}

func (r *Resolver) nodeBelongsToScope(
	ctx context.Context,
	candidate *node.Node,
	scopeID uuid.UUID,
) bool {
	if candidate == nil || scopeID == uuid.Nil {
		return false
	}

	if got := rawUUID(candidate.Props, "scope_id"); got == scopeID {
		return true
	}

	if storedScopeID := rawUUID(candidate.Props, "scope_id"); storedScopeID != uuid.Nil &&
		r.parentChainContains(ctx, storedScopeID, scopeID) {
		return true
	}

	parent, err := readParent(
		ctx,
		r.reader,
		&node.NodeView{
			ID:        candidate.ID,
			OrgID:     candidate.OrgID,
			NodeType:  candidate.NodeType,
			Name:      candidate.Name,
			Props:     candidate.Props,
			CreatedBy: candidate.CreatedBy,
			UpdatedBy: candidate.UpdatedBy,
			CreatedAt: candidate.CreatedAt,
			UpdatedAt: candidate.UpdatedAt,
		},
	)
	if err != nil || parent == nil {
		return false
	}

	parentID := parent.ID

	if parentID == scopeID {
		return true
	}

	return r.parentChainContains(ctx, parentID, scopeID)
}

func (r *Resolver) parentChainContains(ctx context.Context, startID, wantID uuid.UUID) bool {
	currentID := startID
	visited := map[uuid.UUID]struct{}{}

	for currentID != uuid.Nil {
		if currentID == wantID {
			return true
		}

		if _, seen := visited[currentID]; seen {
			return false
		}

		visited[currentID] = struct{}{}
		currentView, err := r.reader.Get(ctx, currentID)
		if err != nil || currentView == nil {
			return false
		}

		parent, err := readParent(ctx, r.reader, currentView)
		if err != nil || parent == nil {
			return false
		}

		currentID = parent.ID
	}

	return false
}

func viewBelongsToScope(
	ctx context.Context,
	resolver *Resolver,
	view *node.NodeView,
	scopeID uuid.UUID,
) bool {
	if view == nil || scopeID == uuid.Nil {
		return false
	}

	if view.ID == scopeID {
		return true
	}

	if uuidProp(view, "scope_id") == scopeID {
		return true
	}

	if storedScopeID := uuidProp(view, "scope_id"); storedScopeID != uuid.Nil &&
		resolver.parentChainContains(ctx, storedScopeID, scopeID) {
		return true
	}

	parent, err := readParent(ctx, resolver.reader, view)
	if err != nil || parent == nil {
		return false
	}

	parentID := parent.ID

	if parentID == scopeID {
		return true
	}

	return resolver.parentChainContains(ctx, parentID, scopeID)
}
