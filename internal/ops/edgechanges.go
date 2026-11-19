package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (c *RepairConsole) plannedParentRelationshipChanges(
	ctx context.Context,
	view *node.NodeView,
	targetID uuid.UUID,
) (node.RelationshipChanges, error) {
	changes := node.RelationshipChanges{Add: nil, Remove: nil}
	targetRelationshipExists := false
	if c.relationships != nil {
		relationships, err := c.relationships.ListBySource(
			ctx,
			view.OrgID,
			view.ID,
			node.RelChildOf,
		)
		if err != nil {
			return changes, loggedRepairError(
				ctx,
				fmt.Sprintf("list child_of relationships for repair %s", view.ID),
				err,
			)
		}

		for _, relationship := range relationships {
			if relationship.TargetID == targetID {
				targetRelationshipExists = true

				continue
			}

			changes.Remove = append(changes.Remove, relationship)
		}
	} else {
		return changes, loggedRepairError(
			ctx,
			fmt.Sprintf(
				"list parent relationships for repair %s: relationship repository required",
				view.ID,
			),
			domain.ErrFailedPrecondition,
		)
	}

	if !targetRelationshipExists {
		changes.Add = append(
			changes.Add,
			&node.Relationship{
				OrgID:        view.OrgID,
				SourceID:     view.ID,
				RelationType: node.RelChildOf,
				TargetID:     targetID,
				CreatedBy:    uuid.Nil,
				CreatedAt:    time.Time{},
				Props:        nil,
			},
		)
	}

	return changes, nil
}
