package main

import (
	"context"
	"log/slog"
	"os"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"github.com/google/uuid"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, nil)
	if err != nil {
		slog.Error("fdb", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Original org and workspace IDs from existing FDB data
	orgID := uuid.MustParse("c5c84639-8e60-4606-b81a-fb0fd6b0b4cb")
	wsID := uuid.MustParse("23e41290-5dd9-4687-a8f8-b31dd58cf17a")

	// Write slug index entries pointing to the original entities
	if err := fdbStores.Entities.WriteSlugIndex(ctx, node.NodeTypeOrg, "my-org", orgID); err != nil {
		slog.Error("write org slug", "err", err)
		os.Exit(1)
	}
	slog.Info("wrote org slug index", "slug", "my-org", "id", orgID)

	if err := fdbStores.Entities.WriteSlugIndex(ctx, node.NodeTypeWorkspace, "main", wsID); err != nil {
		slog.Error("write ws slug", "err", err)
		os.Exit(1)
	}
	slog.Info("wrote workspace slug index", "slug", "main", "id", wsID)
}
