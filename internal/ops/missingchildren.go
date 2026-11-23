package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func createMissingDefaultChild(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	parentID uuid.UUID,
	defaultChild node.DefaultChild,
) (bool, error) {
	query := defaultChildrenQuery(
		orgID,
		defaultChild.TypeKey,
		&node.RelationScan{
			NodeID:       parentID,
			RelationType: node.RelChildOf,
		},
	)
	existing, err := env.Stores.Views.List(ctx, query)
	if err != nil {
		return false, loggedRepairError(
			ctx,
			fmt.Sprintf("list %s children of node %s", defaultChild.TypeKey, parentID),
			err,
		)
	}

	if hasNamed(existing, defaultChild.Name) {
		return false, nil
	}

	child, view, relationships := defaultChildRecords(orgID, parentID, defaultChild)
	if err := env.Stores.Nodes.CreateAtomic(
		ctx,
		child,
		view,
		relationships,
		nil,
		nil,
		nil,
	); err != nil {
		return false, loggedRepairError(
			ctx,
			fmt.Sprintf("create default child %q under node %s", defaultChild.Name, parentID),
			err,
		)
	}

	return true, nil
}

func defaultChildrenQuery(
	orgID uuid.UUID,
	typeKey string,
	parent *node.RelationScan,
) node.NodeListQuery {
	return node.NodeListQuery{
		OrgID:            orgID,
		NodeType:         typeKey,
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: parent,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            0,
		Cursor:           "",
	}
}
