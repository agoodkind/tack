package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func advancePlannedMutation(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
	result *node.Node,
) error {
	planned, err := json.Marshal(result)
	if err != nil {
		return nodeOperationFailure(ctx, "encode node "+plan.NodeID.String(), err)
	}

	return advanceMutationCheckpoint(ctx, transaction, plan.Checkpoint, planned)
}

func appliedMutationReceipt(
	plan node.MutationPlan,
	source mutationSource,
	encoded []byte,
) node.MutationReceipt {
	receipt := node.MutationReceipt{
		OrgID:        plan.OrgID,
		NodeID:       plan.NodeID,
		SourceDigest: node.Digest{},
		ResultDigest: node.PrimaryDigest(encoded),
	}
	if source.Exists {
		receipt.SourceDigest = node.PrimaryDigest(source.Encoding)
	}

	return receipt
}
