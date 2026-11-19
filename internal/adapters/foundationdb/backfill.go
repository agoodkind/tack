package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

const legacyParentProperty = "parent_id"

const parentMigrationNodeTupleSize = 4

// ParentRelationshipPage reports one bounded page of the parent migration.
// Changed counts nodes that need a missing child_of relationship.
// Added counts missing child_of relationships. Next selects the following page.
type ParentRelationshipPage struct {
	Scanned int
	Changed int
	Added   int
	Next    string
}

// BackfillParentRelationships creates missing child_of relationships.
//
// Each node commits its relationship and audit evidence together.
// With write false, it validates and counts changes without modifying records.
//
// For example, a dry run starts with an empty cursor:
//
//	page, err := store.BackfillParentRelationships(ctx, "", false)
func (s *NodeStore) BackfillParentRelationships(
	ctx context.Context,
	cursor string,
	write bool,
) (ParentRelationshipPage, error) {
	var operationErr error
	defer telemetry.FDBOp(ctx, "store.node.backfill_parent_relationships")(&operationErr)
	page, operationErr := s.backfillParentRelationships(ctx, cursor, write)

	return page, operationErr
}

func (s *NodeStore) backfillParentRelationships(
	ctx context.Context,
	cursor string,
	write bool,
) (ParentRelationshipPage, error) {
	page := ParentRelationshipPage{Scanned: 0, Changed: 0, Added: 0, Next: ""}
	var items []fdb.KeyValue
	err := runNodeReadTransaction(
		ctx,
		s.db,
		"scan legacy parent properties",
		func(transaction fdb.Transaction) error {
			var readErr error
			items, page.Next, readErr = readNameIndexRecords(
				ctx,
				transaction,
				keyNodeInstance,
				cursor,
			)

			return readErr
		},
	)
	if err != nil {
		return page, err
	}

	for _, item := range items {
		var current node.Node
		if err := json.Unmarshal(item.Value, &current); err != nil {
			return page, nodeOperationFailure(ctx, "decode a node for parent migration", err)
		}

		if err := checkParentMigrationIdentity(ctx, item.Key, &current); err != nil {
			return page, err
		}

		changed, added, err := s.migrateParentRelationship(
			ctx,
			current.OrgID,
			current.ID,
			current.NodeType,
			write,
		)
		if err != nil {
			return page, err
		}

		page.Scanned++
		if changed {
			page.Changed++
		}

		if added {
			page.Added++
		}
	}

	return page, nil
}

func checkParentMigrationIdentity(ctx context.Context, key fdb.Key, current *node.Node) error {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return nodeOperationFailure(ctx, "decode a primary key for parent migration", err)
	}

	if len(values) != parentMigrationNodeTupleSize || values[1] != current.OrgID.String() ||
		values[2] != current.NodeType || values[3] != current.ID.String() {
		return mutationRefusal(
			ctx,
			current.ID,
			"the primary key and node identity disagree; repair the record before migrating parents",
			domain.ErrInvalidArgument,
		)
	}

	return nil
}
