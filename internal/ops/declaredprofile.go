package ops

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func prepareParentReferenceProfile(
	ctx context.Context,
	profile *RepairReferenceProfile,
) (RepairReferenceProfile, error) {
	if profile == nil {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair profile required",
			domain.ErrInvalidArgument,
		)
	}

	prepared := *profile
	prepared.TargetProperty = strings.TrimSpace(prepared.TargetProperty)
	if prepared.TargetProperty == "" {
		prepared.TargetProperty = repairParentProperty
	}

	if prepared.TargetProperty != repairParentProperty {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair target_property must be parent_id",
			domain.ErrInvalidArgument,
		)
	}

	prepared.TargetTypeKey = strings.TrimSpace(prepared.TargetTypeKey)
	if prepared.TargetTypeKey == "" {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair target_type_key required",
			domain.ErrInvalidArgument,
		)
	}

	prepared.TargetMatchProperty = strings.TrimSpace(prepared.TargetMatchProperty)
	prepared.SourceFields = compactStrings(prepared.SourceFields)
	if len(prepared.SourceFields) == 0 {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair source_fields required",
			domain.ErrInvalidArgument,
		)
	}

	prepared.ScopeFields = compactStrings(prepared.ScopeFields)
	if len(prepared.ScopeFields) == 0 {
		prepared.ScopeFields = []string{"scope_id", repairParentProperty}
	}

	if prepared.CleanupBehavior == "" {
		prepared.CleanupBehavior = RepairCleanupSourceFields
	}

	if prepared.ConflictPolicy == "" {
		prepared.ConflictPolicy = RepairConflictPreferSource
	}

	return prepared, nil
}

func plannedParentReferenceProps(
	view *node.NodeView,
	profile RepairReferenceProfile,
	chosenID uuid.UUID,
) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage)
	if rawUUIDProp(view.Props, repairParentProperty) != chosenID {
		props[repairParentProperty] = service.MustRawString(chosenID.String())
	}

	if profile.CleanupBehavior == RepairCleanupSourceFields {
		for _, field := range profile.SourceFields {
			if _, ok := view.Props[field]; ok {
				props[field] = json.RawMessage(transformedNullProperty)
			}
		}
	}

	return props
}
