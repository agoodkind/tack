package ops

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "reindex",
		Description: "Backfill secondary property index entries for every existing node. Idempotent; safe to re-run.",
		Run:         runReindex,
	})
}

func runReindex(ctx context.Context, env *Env) error {
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return err
	}

	for orgID := range orgIDs {
		types, err := env.Stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "reindex: list node types",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			continue
		}
		defs, err := env.Stores.PropertyDefs.List(ctx, orgID)
		if err != nil {
			env.Log.ErrorContext(ctx, "reindex: list property defs",
				slog.String("org_id", orgID.String()),
				slog.String("err", err.Error()))
			continue
		}

		for _, nt := range types {
			indexed := indexedPropsFor(nt, defs)
			if len(indexed) == 0 {
				continue
			}

			views, err := env.Stores.Views.List(ctx, node.NodeListQuery{
				OrgID:    orgID,
				NodeType: nt.TypeKey,
			})
			if err != nil {
				env.Log.ErrorContext(ctx, "reindex: list views",
					slog.String("org_id", orgID.String()),
					slog.String("type", nt.TypeKey),
					slog.String("err", err.Error()))
				continue
			}

			for _, v := range views {
				n, err := env.Stores.Nodes.Get(ctx, orgID, v.ID)
				if err != nil || n == nil {
					continue
				}
				if err := env.Stores.Nodes.EnsurePropertyIndex(ctx, n, indexed); err != nil {
					env.Log.WarnContext(ctx, "reindex: write index",
						slog.String("node_id", n.ID.String()),
						slog.String("type", nt.TypeKey),
						slog.String("err", err.Error()))
					continue
				}
			}

			env.Log.DebugContext(ctx, "reindex.type",
				slog.String("org_id", orgID.String()),
				slog.String("type", nt.TypeKey),
				slog.Int("nodes", len(views)),
				slog.Any("props", indexed))
		}
	}
	return nil
}

// listOrgIDs reads every distinct org_id from postgres org_members. FDB has
// no scan-all-orgs path because every entity store keys on orgID.
func listOrgIDs(ctx context.Context, env *Env) (map[uuid.UUID]struct{}, error) {
	rows, err := env.Pool.Query(ctx, "SELECT DISTINCT org_id FROM org_members")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orgIDs := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			env.Log.ErrorContext(ctx, "ops: scan org id", slog.String("err", err.Error()))
			continue
		}
		orgIDs[id] = struct{}{}
	}
	return orgIDs, nil
}

// indexedPropsFor returns the names of indexed PropertyDefs that apply to nt.
// Mirrors the same predicate the service layer uses at create time so the
// backfill writes exactly the indexes a fresh create would.
func indexedPropsFor(nt *node.NodeType, defs []*node.PropertyDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if !d.Indexed {
			continue
		}
		if len(d.AppliesToFeatures) == 0 || nt.Features.HasAny(d.AppliesToFeatures...) {
			out = append(out, d.Name)
		}
	}
	return out
}
