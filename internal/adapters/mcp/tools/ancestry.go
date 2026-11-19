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

// ancestorOfType returns the first child_of ancestor with level.TypeKey.
// It returns an error if a read fails or the parent chain ends or repeats
// a node without a match.
func (r *Resolver) ancestorOfType(
	ctx context.Context,
	start *node.NodeView,
	level ScopeLevel,
) (*node.NodeView, error) {
	visited := map[uuid.UUID]struct{}{start.ID: {}}

	current := start

	for {
		view, err := readParent(ctx, r.reader, current)
		if err != nil {
			return nil, err
		}

		if view == nil {
			break
		}

		currentID := view.ID
		if _, seen := visited[currentID]; seen {
			break
		}

		visited[currentID] = struct{}{}
		if view.NodeType == level.TypeKey {
			return view, nil
		}

		current = view
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
