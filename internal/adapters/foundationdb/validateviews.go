package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// CheckParentMigrationViews validates one bounded page of legacy read views.
//
// Each view with parent_id must match its primary and resolution records.
// The returned cursor selects the next page and is empty after the last page.
//
// An empty cursor starts validation:
//
//	next, err := store.CheckParentMigrationViews(ctx, "")
func (s *NodeStore) CheckParentMigrationViews(
	ctx context.Context,
	cursor string,
) (string, error) {
	next := ""
	err := runNodeReadTransaction(
		ctx,
		s.db,
		"validate legacy parent read views",
		func(transaction fdb.Transaction) error {
			items, pageNext, readErr := readNameIndexRecords(ctx, transaction, keyNodeView, cursor)
			if readErr != nil {
				return readErr
			}

			next = pageNext

			for _, item := range items {
				if err := checkParentMigrationView(ctx, transaction, item); err != nil {
					return err
				}
			}

			return nil
		},
	)
	if err != nil {
		return "", err
	}

	return next, nil
}

func checkParentMigrationView(
	ctx context.Context,
	transaction fdb.Transaction,
	item fdb.KeyValue,
) error {
	var view node.NodeView
	if err := json.Unmarshal(item.Value, &view); err != nil {
		return nodeOperationFailure(ctx, "decode a read view for parent migration", err)
	}

	if !bytes.Equal(item.Key, nodeViewKey(view.OrgID, view.NodeType, view.ID)) {
		return mutationRefusal(
			ctx,
			view.ID,
			"the legacy read view key and identity disagree; repair the read view before migrating parents",
			domain.ErrInvalidArgument,
		)
	}

	source, err := readMutationSource(ctx, transaction, view.OrgID, view.ID)
	if err != nil {
		return err
	}

	if !source.Exists || source.TypeKey != view.NodeType ||
		source.Node.ID != view.ID || source.Node.OrgID != view.OrgID ||
		source.Node.NodeType != view.NodeType {
		return mutationRefusal(
			ctx,
			view.ID,
			"the legacy read view lacks matching primary and resolution records; repair the records before migrating parents",
			domain.ErrInvalidArgument,
		)
	}

	return checkAdditiveParentParity(ctx, transaction, &source.Node, &view)
}
