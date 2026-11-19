package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ApplyMutation creates or updates one node in one transaction. Each attempt
// rereads the idempotency record, source primary record, metadata epoch,
// expected graph records, and parent chain before writing. The attempt
// writes
// the primary, view, and resolution records, property indexes, reference
// claims,
// relationships, search work, and one change event for each changed node or
// relationship.
func (s *NodeStore) ApplyMutation(
	ctx context.Context,
	plan node.MutationPlan,
) (receipt node.MutationReceipt, err error) {
	defer telemetry.FDBOp(ctx, "store.node.apply_mutation")(&err)
	changes, err := beginMutationAudit(ctx, plan.OrgID)
	if err != nil {
		return node.MutationReceipt{}, err
	}

	err = runNodeMutation(
		ctx,
		s.db,
		"apply mutation to node "+plan.NodeID.String(),
		func(transaction fdb.Transaction) error {
			changes.startAttempt()
			var attemptErr error
			receipt, attemptErr = s.applyMutationAttempt(ctx, transaction, plan, changes)
			if attemptErr != nil {
				return attemptErr
			}

			return changes.append(ctx, transaction)
		},
	)
	if err != nil {
		return node.MutationReceipt{}, err
	}

	changes.finish(ctx)

	return receipt, nil
}

// applyMutationAttempt returns the receipt without writing when the
// idempotency
// record exists. Otherwise, it applies plan inside transaction, records node
// and relationship
// changes in changes, and returns a receipt for the write.
func (s *NodeStore) applyMutationAttempt(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
	changes *mutationAudit,
) (node.MutationReceipt, error) {
	none := node.MutationReceipt{
		OrgID:        plan.OrgID,
		NodeID:       plan.NodeID,
		SourceDigest: node.Digest{},
		ResultDigest: node.Digest{},
	}
	existing, err := readCreateIdempotency(ctx, transaction, plan)
	if err != nil {
		return none, err
	}

	if existing != nil {
		return *existing, nil
	}

	source, err := readMutationSource(ctx, transaction, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}

	if err := checkSource(ctx, plan.NodeID, plan.Source, source); err != nil {
		return none, err
	}

	if err := checkPlanShape(ctx, plan, source); err != nil {
		return none, err
	}

	schema, err := checkMutationSchema(ctx, transaction, plan)
	if err != nil {
		return none, err
	}

	records, err := checkGraphRecords(ctx, transaction, plan.OrgID, plan.NodeID, plan.Graph.Records)
	if err != nil {
		return none, err
	}

	if plan.Parent != nil {
		if err := checkParentChain(
			ctx,
			transaction,
			plan.OrgID,
			plan.NodeID,
			plan.Parent.TargetID,
			records,
		); err != nil {
			return none, err
		}
	}

	result := mutationResult(plan, source, s.clock.Now().UTC())
	if err := checkMoveScope(ctx, transaction, plan, source, result, records); err != nil {
		return none, err
	}

	parentChanges, err := childOfChanges(ctx, transaction, plan, source, &result)
	if err != nil {
		return none, err
	}

	strict, err := s.fenceAttempt(ctx, transaction, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}

	if err := checkStrictMutation(ctx, transaction, strict, &result, plan.Parent); err != nil {
		return none, err
	}

	if err := checkMutationResult(ctx, transaction, plan, source, &result, schema); err != nil {
		return none, err
	}

	relationshipChanges := []node.RelationshipChanges{planRelationshipChanges(plan), parentChanges}
	encoded, err := s.finishMutationWrite(
		ctx,
		transaction,
		plan,
		source,
		&result,
		schema,
		relationshipChanges,
		strict,
		changes,
	)
	if err != nil {
		return none, err
	}

	return writtenMutationReceipt(plan, source, encoded), nil
}
