package temporal

import (
	"context"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/node"
)

// Activities holds dependencies injected into Temporal activity functions.
type Activities struct {
	NodeDeleter node.NodeDeleter
}

// DeleteNodeActivity is the Temporal activity that calls NodeDeleter.DeleteNode.
// It runs inside the Temporal worker and is retried automatically on failure.
func (a *Activities) DeleteNodeActivity(ctx context.Context, input NodeCleanupInput) error {
	if err := a.NodeDeleter.DeleteNode(ctx, input.OrgID, input.NodeID); err != nil {
		return fmt.Errorf("delete node %s: %w", input.NodeID, err)
	}
	return nil
}
