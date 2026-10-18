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
// relationships, search work, and audit intent.
func (s *NodeStore) ApplyMutation(ctx context.Context, plan node.MutationPlan) (receipt node.MutationReceipt, err error) {
	defer telemetry.FDBOp(ctx, "store.node.apply_mutation")(&err)
	written := false
	err = runNodeMutation(ctx, s.db, "apply mutation to node "+plan.NodeID.String(), func(tr fdb.Transaction) error {
		var attemptErr error
		receipt, written, attemptErr = s.applyMutationAttempt(ctx, tr, plan)
		return attemptErr
	})
	if err != nil {
		return node.MutationReceipt{}, err
	}
	if written {
		commitStagedIntent(ctx)
	}
	return receipt, nil
}

// applyMutationAttempt runs one attempt of plan inside tr. It returns false
// when it returns a receipt from an existing idempotency record without
// writing.
func (s *NodeStore) applyMutationAttempt(ctx context.Context, tr fdb.Transaction, plan node.MutationPlan) (node.MutationReceipt, bool, error) {
	none := node.MutationReceipt{OrgID: plan.OrgID, NodeID: plan.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.Digest{}}
	existing, err := readCreateIdempotency(ctx, tr, plan)
	if err != nil {
		return none, false, err
	}
	if existing != nil {
		return *existing, false, nil
	}
	source, err := readMutationSource(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, false, err
	}
	if err := checkSource(ctx, plan.NodeID, plan.Source, source); err != nil {
		return none, false, err
	}
	if err := checkPlanShape(ctx, plan, source); err != nil {
		return none, false, err
	}
	schema, err := checkMutationSchema(ctx, tr, plan)
	if err != nil {
		return none, false, err
	}
	records, err := checkGraphRecords(ctx, tr, plan.OrgID, plan.NodeID, plan.Graph.Records)
	if err != nil {
		return none, false, err
	}
	if plan.Parent != nil {
		if err := checkParentChain(ctx, tr, plan.OrgID, plan.NodeID, plan.Parent.TargetID, records); err != nil {
			return none, false, err
		}
	}
	result := mutationResult(plan, source, s.clock.Now().UTC())
	if err := checkMoveScope(ctx, plan, source, result, records); err != nil {
		return none, false, err
	}
	parentChanges, err := childOfChanges(ctx, tr, plan, source, &result)
	if err != nil {
		return none, false, err
	}
	strict, err := s.fenceAttempt(ctx, tr, plan.OrgID, plan.NodeID)
	if err != nil {
		return none, false, err
	}
	if err := refuseStrictResult(ctx, plan.NodeID, strict); err != nil {
		return none, false, err
	}
	encoded, err := json.Marshal(&result)
	if err != nil {
		return none, false, nodeOperationFailure(ctx, "encode node "+plan.NodeID.String(), err)
	}
	changes := []node.RelationshipChanges{planRelationshipChanges(plan), parentChanges}
	if err := s.writeMutation(ctx, tr, plan, source, &result, schema, changes); err != nil {
		return none, false, err
	}
	if err := writeMutationIntent(ctx, tr, source.Encoding, encoded); err != nil {
		return none, false, err
	}
	receipt := node.MutationReceipt{OrgID: plan.OrgID, NodeID: plan.NodeID, SourceDigest: node.Digest{}, ResultDigest: node.PrimaryDigest(encoded)}
	if source.Exists {
		receipt.SourceDigest = node.PrimaryDigest(source.Encoding)
	}
	return receipt, true, nil
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
func (s *NodeStore) writeMutation(
	ctx context.Context, tr fdb.Transaction, plan node.MutationPlan, source mutationSource, result *node.Node,
	schema node.Schema, changes []node.RelationshipChanges,
) error {
	if err := writeNodeRecords(ctx, tr, result, mutationView(result)); err != nil {
		return err
	}
	reconcilePropertyIndexes(tr, result, source.Node.Props, indexedPropertyNames(schema, schema.Types[plan.NodeType]))
	if source.Exists && plan.ReferenceKeys != nil {
		if err := writeReferenceKeys(tr, plan.OrgID, plan.NodeID, plan.ReferenceKeys); err != nil {
			return err
		}
	}
	if !source.Exists {
		if err := claimReferenceKeys(tr, plan.OrgID, plan.NodeID, plan.ReferenceKeys); err != nil {
			return err
		}
	}
	if err := applyRelationshipChanges(ctx, tr, changes); err != nil {
		return err
	}
	if err := writeCreateIdempotencyRecord(ctx, tr, plan); err != nil {
		return err
	}
	if !s.searchWork {
		return nil
	}
	now := s.clock.Now()
	if _, err := scheduleSearchChange(ctx, tr, now, plan.OrgID, plan.NodeID, mutationSearchChange(source, result, schema.Definitions)); err != nil {
		return err
	}
	if !source.Exists {
		return nil
	}
	return scheduleRelatedSearchWork(ctx, tr, now, changes)
}
