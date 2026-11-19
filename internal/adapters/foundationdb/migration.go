package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

func (s *NodeStore) migrateParentRelationship(
	ctx context.Context,
	orgID uuid.UUID,
	nodeID uuid.UUID,
	nodeType string,
	write bool,
) (bool, bool, error) {
	var changes *mutationAudit
	var err error
	if write {
		changes, err = beginMutationAudit(ctx, orgID)
		if err != nil {
			return false, false, err
		}
	}

	changed, added := false, false
	attempt := func(transaction fdb.Transaction) error {
		changed, added = false, false
		if changes != nil {
			changes.startAttempt()
		}

		record, readErr := s.readParentMigration(ctx, transaction, orgID, nodeID, nodeType)
		if readErr != nil || record == nil {
			return readErr
		}

		changed, added = true, record.relationship != nil
		if !write {
			return nil
		}

		return s.applyParentMigration(ctx, transaction, record, changes)
	}
	if write {
		err = runNodeMutation(ctx, s.db, "migrate parent of node "+nodeID.String(), attempt)
	} else {
		err = runNodeReadTransaction(ctx, s.db, "preview parent of node "+nodeID.String(), attempt)
	}

	if err == nil && changes != nil {
		changes.finish(ctx)
	}

	return changed, added, err
}
