package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (c *RepairConsole) loadReferenceRepairMetadata(
	ctx context.Context,
	view *node.NodeView,
	profile RepairReferenceProfile,
	allowExplicitTargetType bool,
) (*referenceRepairMetadata, error) {
	types, err := c.nodeTypes.List(ctx, view.OrgID)
	if err != nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("list node types for repair %s", view.ID),
			err,
		)
	}

	typeIndex := node.BuildTypeIndex(types)
	currentType := typeIndex[view.NodeType]
	if currentType == nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("node type %q for repair %s", view.NodeType, view.ID),
			domain.ErrNotFound,
		)
	}

	scopeID, err := repairScopeID(ctx, c.reader, view, profile.ScopeFields)
	if err != nil {
		return nil, err
	}

	if allowExplicitTargetType && strings.TrimSpace(profile.TargetTypeKey) != "" {
		targetType := typeIndex[strings.TrimSpace(profile.TargetTypeKey)]
		if targetType == nil {
			return nil, loggedRepairError(
				ctx,
				fmt.Sprintf(
					"reference target type %q for repair %s",
					profile.TargetTypeKey,
					view.ID,
				),
				domain.ErrNotFound,
			)
		}

		return &referenceRepairMetadata{
			typeIndex:  typeIndex,
			targetDef:  nil,
			targetType: targetType,
			scopeID:    scopeID,
		}, nil
	}

	defs, err := c.propertyDefs.List(ctx, view.OrgID)
	if err != nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("list property defs for repair %s", view.ID),
			err,
		)
	}

	targetDef, err := referenceTargetDef(ctx, defs, currentType, profile.TargetProperty)
	if err != nil {
		return nil, err
	}

	targetType := typeIndex[targetDef.ReferenceTargetTypeKey]
	if targetType == nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf(
				"reference target type %q for repair %s",
				targetDef.ReferenceTargetTypeKey,
				view.ID,
			),
			domain.ErrNotFound,
		)
	}

	return &referenceRepairMetadata{
		typeIndex:  typeIndex,
		targetDef:  targetDef,
		targetType: targetType,
		scopeID:    scopeID,
	}, nil
}

func referenceTargetDef(
	ctx context.Context,
	defs []*node.PropertyDef,
	currentType *node.NodeType,
	targetProperty string,
) (*node.PropertyDef, error) {
	for _, def := range defs {
		if def.Name != targetProperty || !node.PropertyAppliesTo(def, currentType) {
			continue
		}

		if def.Type != node.PropertyTypeUUID ||
			strings.TrimSpace(def.ReferenceTargetTypeKey) == "" {
			return nil, loggedRepairError(
				ctx,
				fmt.Sprintf("reference property %q has invalid definition", targetProperty),
				domain.ErrFailedPrecondition,
			)
		}

		return def, nil
	}

	return nil, loggedRepairError(
		ctx,
		fmt.Sprintf(
			"reference property %q is not declared for node type %q",
			targetProperty,
			currentType.TypeKey,
		),
		domain.ErrNotFound,
	)
}

type referenceRepairMetadata struct {
	typeIndex  map[string]*node.NodeType
	targetDef  *node.PropertyDef
	targetType *node.NodeType
	scopeID    uuid.UUID
}
