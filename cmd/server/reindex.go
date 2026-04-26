package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
)

// runReindex backfills missing secondary property indexes for every node in
// every org. Safe to re-run.
//
// Use case: a PropertyDef was added with Indexed=true after nodes had already
// been created. CreateAtomic only writes secondary indexes for the indexed
// PropertyDefs that existed at creation time, so old nodes are missing the
// index entry and cannot be found via ListByProperty.
func runReindex(cfg *config.Config) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		slog.Error("reindex: postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("reindex: foundationdb", "err", err)
		os.Exit(1)
	}

	// Discover orgs via the SQL auth gate. org_members carries every org id
	// the system knows about. NodeListView scans require an OrgID upfront,
	// so we need a separate enumeration source here.
	rows, err := pool.Query(ctx, "SELECT DISTINCT org_id FROM org_members")
	if err != nil {
		slog.Error("reindex: list orgs", "err", err)
		os.Exit(1)
	}
	defer rows.Close()
	orgIDs := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			slog.Error("reindex: scan org id", "err", err)
			continue
		}
		orgIDs[id] = struct{}{}
	}

	for orgID := range orgIDs {
		// Load NodeTypes and PropertyDefs for this org.
		types, err := stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			slog.Error("reindex: list node types", "org_id", orgID, "err", err)
			continue
		}
		defs, err := stores.PropertyDefs.List(ctx, orgID)
		if err != nil {
			slog.Error("reindex: list property defs", "org_id", orgID, "err", err)
			continue
		}

		for _, nt := range types {
			indexed := indexedPropsFor(nt, defs)
			if len(indexed) == 0 {
				continue
			}

			// List all nodes of this type via the view scan.
			views, err := stores.Views.List(ctx, node.NodeListQuery{
				OrgID:    orgID,
				NodeType: nt.TypeKey,
			})
			if err != nil {
				slog.Error("reindex: list views",
					"org_id", orgID, "type", nt.TypeKey, "err", err)
				continue
			}

			for _, v := range views {
				// Read the primary record so we have authoritative Props.
				n, err := stores.Nodes.Get(ctx, orgID, v.ID)
				if err != nil || n == nil {
					continue
				}
				if err := stores.Nodes.EnsurePropertyIndex(ctx, n, indexed); err != nil {
					slog.Warn("reindex: write index",
						"node_id", n.ID, "type", nt.TypeKey, "err", err)
					continue
				}
			}

			slog.Info("reindex.type",
				slog.String("org_id", orgID.String()),
				slog.String("type", nt.TypeKey),
				slog.Int("nodes", len(views)),
				slog.Any("props", indexed),
			)
		}
	}

	slog.Info("reindex complete")
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
