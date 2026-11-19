package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func (s *NodeStore) applyParentMigration(
	ctx context.Context,
	transaction fdb.Transaction,
	record *parentMigration,
	changes *mutationAudit,
) error {
	current := record.current
	if _, err := s.fenceAttempt(ctx, transaction, current.OrgID, current.ID); err != nil {
		return err
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
