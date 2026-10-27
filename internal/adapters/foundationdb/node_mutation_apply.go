package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ApplyMutation creates or updates one node in one transaction. Each attempt
// rereads the idempotency record, source primary record, metadata epoch,
// expected graph records, and parent chain before writing. The attempt writes
// the primary, view, and resolution records, property indexes, reference claims,
// relationships, search work, and one change event for each changed node or relationship.
func (s *NodeStore) ApplyMutation(ctx context.Context, plan node.MutationPlan) (receipt node.MutationReceipt, err error) {
	defer telemetry.FDBOp(ctx, "store.node.apply_mutation")(&err)
	changes, err := beginMutationAudit(ctx, plan.OrgID)
	if err != nil {
		return node.MutationReceipt{}, err
	}
	err = runNodeMutation(ctx, s.db, "apply mutation to node "+plan.NodeID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		var attemptErr error
		receipt, attemptErr = s.applyMutationAttempt(ctx, tr, plan, changes)
		if attemptErr != nil {
			return attemptErr
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		return node.MutationReceipt{}, err
	}
	changes.finish(ctx)
	return receipt, nil
}

// applyMutationAttempt returns the receipt without writing when the idempotency
// record exists. Otherwise, it applies plan inside tr, records node and relationship
// changes in changes, and returns a receipt for the write.
func (s *NodeStore) applyMutationAttempt(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan, changes *mutationAudit) (node.MutationReceipt, error) {
	none := node.MutationReceipt{OrgID: plan.OrgID, NodeID: plan.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.Digest{}}
	existing, err := readCreateIdempotency(ctx, tr, plan)
	if err != nil {
		return none, err
	}
	if existing != nil {
		return *existing, nil
	}
	source, err := readMutationSource(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}
	if err := checkSource(ctx, plan.NodeID, plan.Source, source); err != nil {
		return none, err
	}
	if err := checkPlanShape(ctx, plan, source); err != nil {
		return none, err
	}
	schema, err := checkMutationSchema(ctx, tr, plan)
	if err != nil {
		return none, err
	}
	records, err := checkGraphRecords(ctx, tr, plan.OrgID, plan.NodeID, plan.Graph.Records)
	if err != nil {
		return none, err
	}
	if plan.Parent != nil {
		if err := checkParentChain(ctx, tr, plan.OrgID, plan.NodeID, plan.Parent.TargetID, records); err != nil {
			return none, err
		}
	}
	result := mutationResult(plan, source, s.clock.Now().UTC())
	if err := checkMoveScope(ctx, plan, source, result, records); err != nil {
		return none, err
	}
	parentChanges, err := childOfChanges(ctx, tr, plan, source, &result)
	if err != nil {
		return none, err
	}
	strict, err := s.fenceAttempt(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, err
	}
	if err := checkStrictMutation(ctx, tr, strict, &result, plan.Parent); err != nil {
		return none, err
	}
	if err := checkMutationResult(ctx, tr, plan, source, &result, schema); err != nil {
		return none, err
	}
	planned, err := json.Marshal(&result)
	if err != nil {
		return none, nodeOperationFailure(ctx, "encode node "+plan.NodeID.String(), err)
	}
	if err := advanceMutationCheckpoint(ctx, tr, plan.Checkpoint, planned); err != nil {
		return none, err
	}
	relationshipChanges := []node.RelationshipChanges{planRelationshipChanges(plan), parentChanges}
	encoded, err := s.writeMutation(ctx, tr, plan, source, &result, schema, relationshipChanges, changes)
	if err != nil {
		return none, err
	}
	if err := checkStrictRelationshipAdds(ctx, tr, strict, relationshipChanges); err != nil {
		return none, err
	}
	receipt := node.MutationReceipt{OrgID: plan.OrgID, NodeID: plan.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.PrimaryDigest(encoded)}
	if source.Exists {
		receipt.SourceDigest = node.PrimaryDigest(source.Encoding)
	}
	return receipt, nil
}

// checkMutationSchema compares the stored metadata epoch with plan and returns
// the node type schema inside tr. It returns an error that wraps
// domain.ErrConflict if the epoch differs, or domain.ErrInvalidArgument if the
// node type does not exist.
func checkMutationSchema(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan) (node.Schema, error) {
	empty := node.Schema{OrgID: plan.OrgID, Types: nil, Definitions: nil}
	epoch, err := readMetadataEpoch(ctx, tr, plan.OrgID)
	if err != nil {
		return empty, err
	}
	if epoch != plan.MetadataEpoch {
		return empty, mutationRefusal(ctx, plan.NodeID, "the metadata epoch changed after preparation", domain.ErrConflict)
	}
	kind, err := cachedNodeTypeByKey(ctx, tr, plan.OrgID, plan.NodeType, make(map[string]*node.NodeType))
	if err != nil {
		return empty, err
	}
	if kind == nil {
		return empty, mutationRefusal(ctx, plan.NodeID, "node type "+plan.NodeType+" does not exist", domain.ErrInvalidArgument)
	}
	definitions, err := readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(plan.OrgID), "property definitions")
	if err != nil {
		return empty, err
	}
	return node.Schema{OrgID: plan.OrgID, Types: map[string]*node.NodeType{kind.TypeKey: kind}, Definitions: definitions}, nil
}

// writeMutation writes the records, property indexes, reference claims,
// relationships, idempotency record, and search work for result inside tr.
// It records the node change and each relationship change in changes and
// returns the node record bytes that it wrote.
func (s *NodeStore) writeMutation(
	ctx context.Context, tr fdb.Transaction, plan node.MutationPlan, source mutationSource, result *node.Node,
	schema node.Schema, relationshipChanges []node.RelationshipChanges, changes *mutationAudit,
) ([]byte, error) {
	encoded, err := writeNodeRecords(ctx, tr, result, mutationView(result))
	if err != nil {
		return nil, err
	}
	target := nodeTarget(result)
	if len(plan.ReferenceKeys) > 0 {
		target.Reference = plan.ReferenceKeys[0].Encoded
	}
	if err := changes.record(target, source.Encoding, encoded); err != nil {
		return nil, nodeOperationFailure(ctx, "record the change of node "+plan.NodeID.String(), err)
	}
	reconcilePropertyIndexes(tr, result, source.Node.Props, indexedPropertyNames(schema, schema.Types[plan.NodeType]))
	if source.Exists && plan.ReferenceKeys != nil {
		if err := writeReferenceKeys(tr, plan.OrgID, plan.NodeID, plan.ReferenceKeys); err != nil {
			return nil, err
		}
	}
	if !source.Exists {
		if err := claimReferenceKeys(tr, plan.OrgID, plan.NodeID, plan.ReferenceKeys); err != nil {
			return nil, err
		}
	}
	if err := applyRelationshipChanges(ctx, tr, relationshipChanges, changes); err != nil {
		return nil, err
	}
	if err := writeCreateIdempotencyRecord(ctx, tr, plan); err != nil {
		return nil, err
	}
	if !s.searchWork {
		return encoded, nil
	}
	now := s.clock.Now()
	if _, err := scheduleSearchChange(ctx, tr, now, plan.OrgID, plan.NodeID, mutationSearchChange(source, result, schema)); err != nil {
		return nil, err
	}
	if !source.Exists {
		return encoded, nil
	}
	return encoded, scheduleRelatedSearchWork(ctx, tr, now, relationshipChanges)
}
