package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (c referenceMatchContext) resolveUUID(
	ctx context.Context,
	nodeID uuid.UUID,
) (*node.NodeView, error) {
	view, err := c.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("get reference node %s", nodeID), err)
	}

	if view == nil || view.OrgID != c.orgID || view.NodeType != nodeTypeKey(c.targetType) {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("reference node %s", nodeID),
			domain.ErrNotFound,
		)
	}

	if c.scopeID != uuid.Nil && scopedReferenceStrategy(c.targetType.Reference.Strategy) &&
		rawUUIDProp(view.Props, "parent_id") != c.scopeID {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("reference node %s is outside scope %s", nodeID, c.scopeID),
			domain.ErrInvalidArgument,
		)
	}

	return view, nil
}

func (c referenceMatchContext) matchScopeReference(ctx context.Context, scopeRef string) error {
	if c.scopeID == uuid.Nil {
		return loggedRepairError(
			ctx,
			fmt.Sprintf("scoped reference %q does not have a repair scope", scopeRef),
			domain.ErrInvalidArgument,
		)
	}

	scopeView, err := c.reader.Get(ctx, c.scopeID)
	if err != nil {
		return loggedRepairError(ctx, fmt.Sprintf("get repair scope %s", c.scopeID), err)
	}

	if scopeView == nil {
		return loggedRepairError(ctx, fmt.Sprintf("repair scope %s", c.scopeID), domain.ErrNotFound)
	}

	scopeType := c.typeIndex[scopeView.NodeType]
	if scopeType == nil {
		return loggedRepairError(
			ctx,
			fmt.Sprintf("repair scope type %q", scopeView.NodeType),
			domain.ErrNotFound,
		)
	}

	if !viewMatchesReference(scopeView, scopeType, scopeRef) {
		return loggedRepairError(
			ctx,
			fmt.Sprintf("scope reference %q does not match scope %s", scopeRef, c.scopeID),
			domain.ErrInvalidArgument,
		)
	}

	return nil
}
