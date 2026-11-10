package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func checkExpectedParent(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
	records map[uuid.UUID]mutationSource,
) error {
	if plan.Parent == nil {
		return nil
	}

	return checkParentChain(
		ctx,
		transaction,
		plan.OrgID,
		plan.NodeID,
		plan.Parent.TargetID,
		records,
	)
}
