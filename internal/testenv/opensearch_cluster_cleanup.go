package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
)

// Close removes this cluster's members and proxy. Process-end Release may
// remove the same containers again; missing containers are already removed.
func (c *OpenSearchCluster) Close(ctx context.Context) error {
	containers := append(slices.Clone(c.members), c.proxy)
	if err := removeContainers(ctx, containers); err != nil {
		slog.ErrorContext(ctx, "testenv.cluster.cleanup_failed", slog.String("cluster", c.name), slog.String("err", err.Error()))
		return fmt.Errorf("remove OpenSearch cluster %s: %w", c.name, err)
	}
	return nil
}
