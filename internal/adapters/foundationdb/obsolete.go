package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func legacyParentID(ctx context.Context, current *node.Node) (uuid.UUID, error) {
	raw := current.Props[legacyParentProperty]
	if len(raw) == 0 {
		return uuid.Nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return uuid.Nil, nodeOperationFailure(
			ctx,
			"decode parent_id of node "+current.ID.String(),
			err,
		)
	}

	parentID, err := uuid.Parse(text)
	if err != nil || parentID == uuid.Nil {
		return uuid.Nil, mutationRefusal(
			ctx,
			current.ID,
			"parent_id must contain a nonzero node UUID; repair the parent before migrating",
			domain.ErrInvalidArgument,
		)
	}

	return parentID, nil
}
