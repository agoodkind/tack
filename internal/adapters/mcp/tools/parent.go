package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
)

var errMultipleParents = errors.New("has multiple parents")

func readParent(
	ctx context.Context,
	reader node.NodeReader,
	view *node.NodeView,
) (*node.NodeView, error) {
	parents, err := reader.List(
		ctx,
		node.NodeListQuery{
			OrgID:            view.OrgID,
			NodeType:         "",
			ByProperty:       nil,
			ByTargetRelation: nil,
			BySourceRelation: &node.RelationScan{NodeID: view.ID, RelationType: node.RelChildOf},
			CreatedAfter:     nil,
			CreatedBefore:    nil,
			PropFilters:      nil,
			Limit:            0,
			Cursor:           "",
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "node.parent_read_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("read parent of node %s: %w", view.ID, err)
	}

	if len(parents) > 1 {
		err := fmt.Errorf("node %s %w", view.ID, errMultipleParents)
		slog.WarnContext(ctx, "node.parents_ambiguous", slog.String("err", err.Error()))

		return nil, err
	}

	if len(parents) == 0 {
		return nil, nil
	}

	return parents[0], nil
}

func (rc *renderCtx) parent(view *node.NodeView) *node.NodeView {
	if rc == nil || rc.reader == nil {
		return nil
	}

	parent, err := readParent(rc.ctx, rc.reader, view)
	if err != nil {
		slog.ErrorContext(rc.ctx, "node.parent_read_failed", slog.String("err", err.Error()))
	}

	return parent
}
