package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func (c referenceMatchContext) resolveHumanReference(
	ctx context.Context,
	ref string,
) (*node.NodeView, error) {
	strategy := c.targetType.Reference.Strategy
	if strategy == node.ReferenceUUIDOnly {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("target type %q only accepts UUID references", nodeTypeKey(c.targetType)),
			domain.ErrInvalidArgument,
		)
	}

	views, err := c.candidateViews(ctx, ref)
	if err != nil {
		return nil, err
	}

	matched := make([]*node.NodeView, 0, len(views))
	for _, view := range views {
		if viewMatchesProfileReference(view, c.targetType, c.matchProperty, ref) {
			matched = append(matched, view)
		}
	}

	sort.Slice(
		matched,
		func(i int, j int) bool { return matched[i].ID.String() < matched[j].ID.String() },
	)
	if len(matched) == 0 {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("reference %q did not match any %s nodes", ref, nodeTypeKey(c.targetType)),
			domain.ErrNotFound,
		)
	}

	if len(matched) > 1 {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("reference %q matched multiple %s nodes", ref, nodeTypeKey(c.targetType)),
			domain.ErrInvalidArgument,
		)
	}

	return matched[0], nil
}

func (c referenceMatchContext) candidateViews(
	ctx context.Context,
	ref string,
) ([]*node.NodeView, error) {
	query := node.NodeListQuery{
		OrgID:            c.orgID,
		NodeType:         nodeTypeKey(c.targetType),
		ByProperty:       nil,
		BySourceRelation: nil,
		ByTargetRelation: nil,
		CreatedAfter:     nil,
		CreatedBefore:    nil,
		PropFilters:      nil,
		Limit:            0,
		Cursor:           "",
	}
	switch c.targetType.Reference.Strategy {
	case node.ReferenceUUIDOnly:
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("target type %q only accepts UUID references", nodeTypeKey(c.targetType)),
			domain.ErrInvalidArgument,
		)
	case node.ReferenceDirectProperty:
		propName := strings.TrimSpace(c.targetType.Reference.Property)
		if propName == "" {
			propName = "slug"
		}

		if propName != "name" {
			query.ByProperty = &node.PropertyMatch{
				PropName: propName,
				Value:    service.MustRawString(ref),
			}
		}
	case node.ReferenceScopedProperty, node.ReferenceScopedSequence:
		if c.scopeID == uuid.Nil {
			return nil, loggedRepairError(
				ctx,
				fmt.Sprintf("target type %q requires repair scope", nodeTypeKey(c.targetType)),
				domain.ErrInvalidArgument,
			)
		}

		query.ByTargetRelation = &node.RelationScan{
			NodeID:       c.scopeID,
			RelationType: node.RelChildOf,
		}
	default:
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf(
				"target type %q reference strategy %q",
				nodeTypeKey(c.targetType),
				c.targetType.Reference.Strategy,
			),
			domain.ErrInvalidArgument,
		)
	}

	views, err := c.reader.List(ctx, query)
	if err != nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("list reference candidates for %q", ref),
			err,
		)
	}

	return views, nil
}
