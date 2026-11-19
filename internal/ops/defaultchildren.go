package ops

import (
	"context"
	"fmt"
	"log/slog"

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
			defaultChildrenQuery(orgID, nodeType.TypeKey, nil),
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

				added, err := createMissingDefaultChild(ctx, env, orgID, parent.ID, defaultChild)
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
