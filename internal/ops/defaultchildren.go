package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// backfillOrgDefaultChildren creates missing default children for every node
// of the given node types in orgID.
//
// backfillOrgDefaultChildren continues with the remaining nodes after each
// failure and returns a slice with one error per failed listing or creation.
func backfillOrgDefaultChildren(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	types []*node.NodeType,
) []error {
	typeIndex := node.BuildTypeIndex(types)
	var failures []error

	for _, nodeType := range types {
		if len(nodeType.DefaultChildren) == 0 {
			continue
		}

		parents, err := env.Stores.Views.List(
			ctx,
			node.NodeListQuery{
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
			},
		)
		if err != nil {
			failures = append(
				failures,
				fmt.Errorf(
					"list nodes of type %q in organization %s: %w",
					nodeType.TypeKey,
					orgID,
					err,
				),
			)

			continue
		}

		created := 0
		skipped := 0

		for _, parent := range parents {
			parentIDRaw := json.RawMessage(strconv.Quote(parent.ID.String()))

			for _, defaultChild := range nodeType.DefaultChildren {
				childType := typeIndex[defaultChild.TypeKey]
				if childType == nil {
					env.Log.WarnContext(
						ctx,
						"backfill: unknown child type",
						slog.String("parent_type", nodeType.TypeKey),
						slog.String("child_type", defaultChild.TypeKey),
					)

					continue
				}

				added, err := createLegacyDefaultChild(
					ctx,
					env,
					orgID,
					parent.ID,
					parentIDRaw,
					defaultChild,
				)
				if err != nil {
					failures = append(failures, err)

					continue
				}

				if added {
					created++
				} else {
					skipped++
				}
			}
		}

		env.Log.DebugContext(
			ctx,
			"backfill.type",
			slog.String("org_id", orgID.String()),
			slog.String("type", nodeType.TypeKey),
			slog.Int("parents", len(parents)),
			slog.Int("children_created", created),
			slog.Int("children_skipped_existing", skipped),
		)
	}

	return failures
}
