package foundationdb

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeStore) applyParentMigration(
	ctx context.Context,
	transaction fdb.Transaction,
	record *parentMigration,
	changes *mutationAudit,
) error {
	current, view := record.current, record.view
	if _, err := s.fenceAttempt(ctx, transaction, current.OrgID, current.ID); err != nil {
		return err
	}

	clearLegacyParentIndex(transaction, current, current.Props[legacyParentProperty])
	current.Props = maps.Clone(current.Props)
	delete(current.Props, legacyParentProperty)
	if view != nil {
		clearLegacyParentIndex(transaction, current, view.Props[legacyParentProperty])
		view.Props = maps.Clone(view.Props)
		delete(view.Props, legacyParentProperty)
	}

	encoded, err := writeNodeRecords(ctx, transaction, current, view)
	if err != nil {
		return err
	}

	if err := changes.record(nodeTarget(current), record.source, encoded); err != nil {
		return nodeOperationFailure(
			ctx,
			"record parent migration of node "+current.ID.String(),
			err,
		)
	}

	var relationships []node.RelationshipChanges
	if record.relationship != nil {
		record.relationship.CreatedBy = changes.operation.Actor.ID
		relationships = []node.RelationshipChanges{
			{Add: []*node.Relationship{record.relationship}, Remove: nil},
		}
		if err := applyRelationshipChanges(ctx, transaction, relationships, changes); err != nil {
			return err
		}
	}

	if err := s.scheduleParentMigration(ctx, transaction, current, relationships); err != nil {
		return err
	}

	return changes.append(ctx, transaction)
}

func (s *NodeStore) scheduleParentMigration(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	relationships []node.RelationshipChanges,
) error {
	if !s.searchWork {
		return nil
	}

	now := s.clock.Now()
	if _, err := scheduleSearchChange(
		ctx,
		transaction,
		now,
		current.OrgID,
		current.ID,
		searchChangeContent,
	); err != nil {
		return err
	}

	return scheduleRelatedSearchWork(ctx, transaction, now, relationships)
}

func clearLegacyParentIndex(transaction fdb.Transaction, current *node.Node, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}

	transaction.Clear(
		fdb.Key(
			nodeByPropertyKey(
				current.OrgID,
				current.NodeType,
				legacyParentProperty,
				encodePropertyValue(raw),
				current.ID,
			),
		),
	)
}
