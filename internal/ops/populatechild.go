package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func createLegacyDefaultChild(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	parentID uuid.UUID,
	parentRaw json.RawMessage,
	defaultChild node.DefaultChild,
) (bool, error) {
	existing, err := env.Stores.Nodes.ListByProperty(
		ctx,
		orgID,
		defaultChild.TypeKey,
		repairParentProperty,
		parentRaw,
	)
	if err != nil {
		slog.ErrorContext(ctx, "backfill.child_list_failed", slog.String("err", err.Error()))

		return false, fmt.Errorf(
			"list %s children of node %s: %w",
			defaultChild.TypeKey,
			parentID,
			err,
		)
	}

	if hasNamed(existing, defaultChild.Name) {
		return false, nil
	}

	child, view, relationships := defaultChildRecords(orgID, parentID, defaultChild)
	if err := env.Stores.Nodes.CreateAtomic(
		ctx,
		child,
		view,
		relationships,
		[]string{repairParentProperty},
		nil,
		nil,
	); err != nil {
		slog.ErrorContext(ctx, "backfill.child_create_failed", slog.String("err", err.Error()))

		return false, fmt.Errorf(
			"create default child %q under node %s: %w",
			defaultChild.Name,
			parentID,
			err,
		)
	}

	return true, nil
}
