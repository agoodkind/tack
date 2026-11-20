package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func scopeRepairViews(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeKey string,
) ([]*node.NodeView, error) {
	views, err := env.Stores.Views.List(ctx, scopeRepairQuery(orgID, typeKey))
	if err != nil {
		wrapped := fmt.Errorf(
			"list %s nodes in organization %s for scope repair: %w",
			typeKey,
			orgID,
			err,
		)
		env.Log.ErrorContext(
			ctx,
			"repair.scope_ids: list views",
			slog.String("org_id", orgID.String()),
			slog.String("type", typeKey),
			slog.String("err", err.Error()),
		)

		return nil, wrapped
	}

	return views, nil
}

func logScopeRepairNodeFailure(
	ctx context.Context,
	env *Env,
	message string,
	current *node.Node,
	failure error,
) {
	env.Log.WarnContext(
		ctx,
		message,
		slog.String("node_id", current.ID.String()),
		slog.String("type", current.NodeType),
		slog.String("err", failure.Error()),
	)
}
