package ops

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func repairScopeID(
	ctx context.Context,
	reader node.NodeReader,
	view *node.NodeView,
	fields []string,
) (uuid.UUID, error) {
	if slices.Contains(fields, obsoleteParentProperty) {
		return uuid.Nil, loggedRepairError(
			ctx,
			"repair scope_fields parent_id is obsolete; remove it to use child_of",
			domain.ErrInvalidArgument,
		)
	}

	for _, field := range fields {
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
