package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func discoverAddressBackfillRows(
	ctx context.Context,
	controls addressBackfillControls,
	env *Env,
) ([]fdbadapter.InspectLegacyAddressRow, bool, error) {
	if controls.NodeID != nil {
		return discoverSingleNodeAddressBackfillRows(ctx, controls, env)
	}
	return discoverBatchAddressBackfillRows(ctx, controls, env)
}

func discoverSingleNodeAddressBackfillRows(
	ctx context.Context,
	controls addressBackfillControls,
	env *Env,
) ([]fdbadapter.InspectLegacyAddressRow, bool, error) {
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, *controls.NodeID)
	if err != nil {
		env.Log.ErrorContext(ctx, "backfill.addresses.discover.node",
			slog.String("node_id", controls.NodeID.String()),
			slog.String("err", err.Error()))
		return nil, false, fmt.Errorf("inspect node %s for address backfill: %w", *controls.NodeID, err)
	}
	rows, truncated := limitAddressBackfillRows(report.LegacyAddressRows, controls.Limit)
	return rows, truncated, nil
}

func discoverBatchAddressBackfillRows(
	ctx context.Context,
	controls addressBackfillControls,
	env *Env,
) ([]fdbadapter.InspectLegacyAddressRow, bool, error) {
	orgIDs, err := sortedAddressBackfillOrgIDs(ctx, env)
	if err != nil {
		return nil, false, err
	}
	rows := make([]fdbadapter.InspectLegacyAddressRow, 0)
	for _, orgID := range orgIDs {
		nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "backfill.addresses.discover.types",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			return nil, false, fmt.Errorf("list node types for org %s: %w", orgID, err)
		}
		sort.Slice(nodeTypes, func(i int, j int) bool {
			return nodeTypes[i].TypeKey < nodeTypes[j].TypeKey
		})
		for _, nodeType := range nodeTypes {
			views, err := env.Stores.Views.List(ctx, node.NodeListQuery{
				OrgID:            orgID,
				NodeType:         nodeType.TypeKey,
				ByProperty:       nil,
				BySourceRelation: nil,
				ByTargetRelation: nil,
				CreatedAfter:     nil,
				CreatedBefore:    nil,
				PropFilters:      nil,
				Limit:            0,
				Cursor:           "",
			})
			if err != nil {
				env.Log.ErrorContext(ctx, "backfill.addresses.discover.views",
					slog.String("org_id", orgID.String()),
					slog.String("node_type", nodeType.TypeKey),
					slog.String("err", err.Error()))
				return nil, false, fmt.Errorf("list %s views for org %s: %w", nodeType.TypeKey, orgID, err)
			}
			sort.Slice(views, func(i int, j int) bool {
				return views[i].ID.String() < views[j].ID.String()
			})
			for _, view := range views {
				report, err := env.Stores.Inspect.QueryNodeRecords(ctx, view.ID)
				if err != nil {
					env.Log.ErrorContext(ctx, "backfill.addresses.discover.inspect",
						slog.String("node_id", view.ID.String()),
						slog.String("err", err.Error()))
					return nil, false, fmt.Errorf("inspect node %s for address backfill: %w", view.ID, err)
				}
				rows = append(rows, report.LegacyAddressRows...)
			}
		}
	}
	rows, truncated := limitAddressBackfillRows(rows, controls.Limit)
	return rows, truncated, nil
}

func sortedAddressBackfillOrgIDs(ctx context.Context, env *Env) ([]uuid.UUID, error) {
	orgIDSet, err := listOrgIDs(ctx, env)
	if err != nil {
		env.Log.ErrorContext(ctx, "backfill.addresses.discover.orgs", slog.String("err", err.Error()))
		return nil, fmt.Errorf("list org ids for address backfill: %w", err)
	}
	orgIDs := make([]uuid.UUID, 0, len(orgIDSet))
	for orgID := range orgIDSet {
		orgIDs = append(orgIDs, orgID)
	}
	sort.Slice(orgIDs, func(i int, j int) bool {
		return orgIDs[i].String() < orgIDs[j].String()
	})
	return orgIDs, nil
}

func limitAddressBackfillRows(
	rows []fdbadapter.InspectLegacyAddressRow,
	limit int,
) ([]fdbadapter.InspectLegacyAddressRow, bool) {
	limitedRows := append([]fdbadapter.InspectLegacyAddressRow(nil), rows...)
	sortLegacyAddressSourceRows(limitedRows)
	truncated := len(limitedRows) > limit
	if truncated {
		limitedRows = limitedRows[:limit]
	}
	return limitedRows, truncated
}
