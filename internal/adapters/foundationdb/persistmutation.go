package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkMutationSchema reads the node type schema inside the transaction.
// It wraps domain.ErrConflict when the metadata epoch differs from the plan
// and domain.ErrInvalidArgument when the node type does not exist.
func checkMutationSchema(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
) (node.Schema, error) {
	empty := node.Schema{OrgID: plan.OrgID, Types: nil, Definitions: nil}
	epoch, err := readMetadataEpoch(ctx, transaction, plan.OrgID)
	if err != nil {
		return empty, err
	}

	if epoch != plan.MetadataEpoch {
		return empty, mutationRefusal(
			ctx,
			plan.NodeID,
			"the metadata epoch changed after preparation",
			domain.ErrConflict,
		)
	}

	kind, err := cachedNodeTypeByKey(
		ctx,
		transaction,
		plan.OrgID,
		plan.NodeType,
		make(map[string]*node.NodeType),
	)
	if err != nil {
		return empty, err
	}

	if kind == nil {
		return empty, mutationRefusal(
			ctx,
			plan.NodeID,
			"node type "+plan.NodeType+" does not exist",
			domain.ErrInvalidArgument,
		)
	}

	definitions, err := readSchemaRecords[node.PropertyDef](
		ctx,
		transaction,
		propertyDefPrefix(plan.OrgID),
		"property definitions",
	)
	if err != nil {
		return empty, err
	}

	return node.Schema{
		OrgID:       plan.OrgID,
		Types:       map[string]*node.NodeType{kind.TypeKey: kind},
		Definitions: definitions,
	}, nil
}

// writeMutation writes the records, property indexes, reference claims,
// relationships, idempotency record, and search work for result inside
// transaction.
// It records the node change and each relationship change in changes and
// returns the node record bytes that it wrote.
func (s *NodeStore) writeMutation(
	ctx context.Context,
	transaction fdb.Transaction,
	plan node.MutationPlan,
	source mutationSource,
	result *node.Node,
	schema node.Schema,
	relationshipChanges []node.RelationshipChanges,
	changes *mutationAudit,
) ([]byte, error) {
	encoded, err := writeNodeRecords(ctx, transaction, result, mutationView(result))
	if err != nil {
		return nil, err
	}

	target := nodeTarget(result)
	if len(plan.ReferenceKeys) > 0 {
		target.Reference = plan.ReferenceKeys[0].Encoded
	}

	if err := changes.record(target, source.Encoding, encoded); err != nil {
		return nil, nodeOperationFailure(
			ctx,
			"record the change of node "+plan.NodeID.String(),
			err,
		)
	}

	reconcilePropertyIndexes(
		transaction,
		result,
		source.Node.Props,
		indexedPropertyNames(schema, schema.Types[plan.NodeType]),
	)
	if source.Exists && plan.ReferenceKeys != nil {
		if err := writeReferenceKeys(
			transaction,
			plan.OrgID,
			plan.NodeID,
			plan.ReferenceKeys,
		); err != nil {
			return nil, err
		}
	}

	if !source.Exists {
		if err := claimReferenceKeys(
			transaction,
			plan.OrgID,
			plan.NodeID,
			plan.ReferenceKeys,
		); err != nil {
			return nil, err
		}
	}

	if err := applyRelationshipChanges(ctx, transaction, relationshipChanges, changes); err != nil {
		return nil, err
	}

	if err := writeCreateIdempotencyRecord(ctx, transaction, plan); err != nil {
		return nil, err
	}

	if !s.searchWork {
		return encoded, nil
	}

	now := s.clock.Now()
	if _, err := scheduleSearchChange(
		ctx,
		transaction,
		now,
		plan.OrgID,
		plan.NodeID,
		mutationSearchChange(source, result, schema),
	); err != nil {
		return nil, err
	}

	if !source.Exists {
		return encoded, nil
	}

	return encoded, scheduleRelatedSearchWork(ctx, transaction, now, relationshipChanges)
}
