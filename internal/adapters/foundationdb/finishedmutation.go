package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeStore) finishMutationWrite(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
	source mutationSource,
	result *node.Node,
	schema node.Schema,
	relationshipChanges []node.RelationshipChanges,
	strict bool,
	changes *mutationAudit,
) ([]byte, error) {
	planned, err := json.Marshal(result)
	if err != nil {
		return nil, nodeOperationFailure(ctx, "encode node "+plan.NodeID.String(), err)
	}

	if err := advanceMutationCheckpoint(ctx, transaction, plan.Checkpoint, planned); err != nil {
		return nil, err
	}

	encoded, err := s.writeMutation(
		ctx,
		transaction,
		plan,
		source,
		result,
		schema,
		relationshipChanges,
		changes,
	)
	if err != nil {
		return nil, err
	}

	if err := checkStrictRelationshipAdds(
		ctx,
		transaction,
		strict,
		relationshipChanges,
	); err != nil {
		return nil, err
	}

	return encoded, nil
}

func writtenMutationReceipt(
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
