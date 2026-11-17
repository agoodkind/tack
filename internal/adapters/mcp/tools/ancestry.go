package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ancestorOfType returns the first parent_id ancestor with level.TypeKey.
// It returns an error if a read fails or the parent chain ends or repeats
// a node without a match.
func (r *Resolver) ancestorOfType(
	ctx context.Context,
	start *node.NodeView,
	level ScopeLevel,
) (*node.NodeView, error) {
	visited := map[uuid.UUID]struct{}{start.ID: {}}

	currentID := uuidProp(start, scopedParentProperty)
	for currentID != uuid.Nil {
		if _, seen := visited[currentID]; seen {
			break
		}

		visited[currentID] = struct{}{}
		view, err := r.reader.Get(ctx, currentID)
		if err != nil {
			wrapped := fmt.Errorf("get ancestor %s of %s: %w", currentID, start.ID, err)
			telemetry.L(ctx).ErrorContext(
				ctx,
				"resolver.scope.ancestor_read_failed",
				slog.String("node_id", start.ID.String()),
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		if view == nil {
			break
		}

		if view.NodeType == level.TypeKey {
			return view, nil
		}

		currentID = uuidProp(view, scopedParentProperty)
	}

	err := fmt.Errorf(
		"%s %s does not have a %s ancestor: %w",
		start.NodeType,
		start.ID,
		level.Slug,
		domain.ErrNotFound,
	)
	telemetry.L(ctx).InfoContext(
		ctx,
		"resolver.scope.ancestor_missing",
		slog.String("node_id", start.ID.String()),
		slog.String("ancestor_type", level.TypeKey),
		slog.String("err", err.Error()),
	)

	return nil, err
}
