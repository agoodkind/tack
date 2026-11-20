package ops

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func repairScopeID(
	ctx context.Context,
	reader node.NodeReader,
	view *node.NodeView,
	fields []string,
) (uuid.UUID, error) {
	for _, field := range fields {
		if field == repairParentProperty {
			parent, err := repairParentView(ctx, reader, view.OrgID, view.ID)
			if err != nil {
				return uuid.Nil, err
			}

			if parent != nil {
				return parent.ID, nil
			}

			continue
		}

		if id := rawUUIDProp(view.Props, field); id != uuid.Nil {
			return id, nil
		}
	}

	parent, err := repairParentView(ctx, reader, view.OrgID, view.ID)
	if err != nil {
		return uuid.Nil, err
	}

	if parent != nil {
		return parent.ID, nil
	}

	return uuid.Nil, nil
}
