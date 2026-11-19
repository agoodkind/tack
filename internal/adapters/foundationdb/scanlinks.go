package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (s *ViewStore) scanByRelation(
	ctx context.Context,
	orgID uuid.UUID,
	nodeType string,
	scan *node.RelationScan,
	fromSource bool,
) ([]*node.NodeView, error) {
	var prefix []byte
	if fromSource {
		prefix = relationshipPrefixBySource(orgID, scan.NodeID, scan.RelationType)
	} else {
		prefix = relationshipReversePrefixByTarget(orgID, scan.NodeID, scan.RelationType)
	}

	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, nodeOperationFailure(
			ctx,
			"create relationship range of node "+scan.NodeID.String(),
			err,
		)
	}

	var views []*node.NodeView
	err = runNodeReadTransaction(
		ctx,
		s.db,
		"scan relationships of node "+scan.NodeID.String(),
		func(transaction fdb.Transaction) error {
			items, readErr := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(
					ctx,
					"read relationships of node "+scan.NodeID.String(),
					readErr,
				)
			}

			views = make([]*node.NodeView, 0, len(items))
			for _, item := range items {
				view, readErr := readRelationView(ctx, transaction, orgID, nodeType, item)
				if readErr != nil {
					return readErr
				}

				if view != nil {
					views = append(views, view)
				}
			}

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return views, nil
}
