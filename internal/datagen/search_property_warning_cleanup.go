package datagen

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// removeWarningNode deletes the warning node through the node service and
// waits for the delete job. It then waits until the warning listing stops
// listing the node. Strict validation rejects the node while it stores a
// property that its definition does not apply to.
func (r searchRun) removeWarningNode(ctx context.Context, nodeID uuid.UUID) error {
	if err := r.fixture.remove(ctx, nodeID); err != nil {
		return err
	}
	if err := r.eventually(ctx, "clear the search warning of the deleted warning node", func(ctx context.Context) error {
		return r.warningCleared(ctx, nodeID)
	}); err != nil {
		return err
	}
	slog.InfoContext(ctx, "qa.datagen.search_warning_node_deleted", slog.String("node_id", nodeID.String()))
	return nil
}

// warningCleared returns an error while the warning listing lists any
// warning for nodeID.
func (r searchRun) warningCleared(ctx context.Context, nodeID uuid.UUID) error {
	cursor := ""
	for {
		page, err := r.fixture.stores.SearchWarnings(ctx, cursor)
		if err != nil {
			return loggedError(ctx, "qa datagen: list search warnings", err)
		}
		for _, warning := range page.Warnings {
			if warning.NodeID == nodeID {
				return fmt.Errorf("search warnings still list property %s on deleted node %s", warning.PropertyName, nodeID)
			}
		}
		if page.Done {
			return nil
		}
		cursor = page.NextCursor
	}
}
