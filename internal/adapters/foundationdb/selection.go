package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func (s *ViewStore) scanCandidates(
	ctx context.Context,
	query node.NodeListQuery,
) ([]*node.NodeView, error) {
	// Primary scan strategies in order of preference.
	switch {
	case query.ByProperty != nil:
		return s.scanByProperty(
			ctx,
			query.OrgID,
			query.NodeType,
			query.ByProperty.PropName,
			query.ByProperty.Value,
		)
	case query.BySourceRelation != nil:
		return s.scanByRelation(ctx, query.OrgID, query.NodeType, query.BySourceRelation, true)
	case query.ByTargetRelation != nil:
		return s.scanByRelation(ctx, query.OrgID, query.NodeType, query.ByTargetRelation, false)
	}

	// Default: full range scan over (orgID, nodeType).
	prefix := nodeViewPrefix(query.OrgID, query.NodeType)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, nodeOperationFailure(
			ctx,
			"create view range for organization "+query.OrgID.String(),
			err,
		)
	}

	var items []fdb.KeyValue
	err = runNodeReadTransaction(
		ctx,
		s.db,
		"scan views of organization "+query.OrgID.String(),
		func(transaction fdb.Transaction) error {
			var readErr error
			items, readErr = transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(
					ctx,
					"read views of organization "+query.OrgID.String(),
					readErr,
				)
			}

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return decodeViews(items), nil
}

func (s *ViewStore) scanByProperty(
	ctx context.Context,
	orgID uuid.UUID,
	nodeType string,
	propName string,
	value json.RawMessage,
) ([]*node.NodeView, error) {
	keyRange, err := fdb.PrefixRange(
		nodeByPropertyValuePrefix(orgID, nodeType, propName, encodePropertyValue(value)),
	)
	if err != nil {
		return nil, nodeOperationFailure(
			ctx,
			"create property range for organization "+orgID.String(),
			err,
		)
	}

	var views []*node.NodeView
	err = runNodeReadTransaction(
		ctx,
		s.db,
		"scan property "+propName+" of organization "+orgID.String(),
		func(transaction fdb.Transaction) error {
			items, readErr := transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(
					ctx,
					"read property index "+propName+" of organization "+orgID.String(),
					readErr,
				)
			}

			views = make([]*node.NodeView, 0, len(items))
			for _, item := range items {
				view, readErr := readPropertyView(ctx, transaction, orgID, nodeType, item)
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

func matchPropFilters(view *node.NodeView, filters []node.PropertyMatch) bool {
	for _, filter := range filters {
		got, found := view.Props[filter.PropName]
		if !found {
			return false
		}

		if !bytes.Equal(got, filter.Value) {
			return false
		}
	}

	return true
}

func decodeViews(items []fdb.KeyValue) []*node.NodeView {
	views := make([]*node.NodeView, 0, len(items))
	for _, item := range items {
		var view node.NodeView
		if err := json.Unmarshal(item.Value, &view); err != nil {
			continue
		}

		views = append(views, &view)
	}

	return views
}
