package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func previewRelationshipChanges(changes node.RelationshipChanges) []RepairRelationshipChange {
	planned := make([]RepairRelationshipChange, 0, len(changes.Remove)+len(changes.Add))
	for _, relationship := range changes.Remove {
		planned = append(
			planned,
			RepairRelationshipChange{
				Action:       RepairRelationshipRemove,
				SourceID:     relationship.SourceID,
				RelationType: relationship.RelationType,
				TargetID:     relationship.TargetID,
			},
		)
	}

	for _, relationship := range changes.Add {
		planned = append(
			planned,
			RepairRelationshipChange{
				Action:       RepairRelationshipAdd,
				SourceID:     relationship.SourceID,
				RelationType: relationship.RelationType,
				TargetID:     relationship.TargetID,
			},
		)
	}

	return planned
}

func summarizeParentReferencePreview(preview *RepairPreview) string {
	if preview == nil || preview.ChosenCandidate == nil {
		return "no parent reference candidate was selected"
	}

	if !preview.NeedsRepair {
		return "parent already matches selected candidate"
	}

	return fmt.Sprintf(
		"repair parent relationship for %s from %s",
		preview.ChosenCandidate.NodeID,
		preview.ChosenCandidate.SourceField,
	)
}

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
		prepared.TargetProperty = node.RelChildOf
	}

	if prepared.TargetProperty != node.RelChildOf {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair target_property must be child_of; update legacy profiles",
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

	if slices.Contains(prepared.SourceFields, obsoleteParentProperty) {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"parent repair source_fields parent_id is obsolete; migrate stored parent_id first",
			domain.ErrInvalidArgument,
		)
	}

	prepared.ScopeFields = compactStrings(prepared.ScopeFields)
	if len(prepared.ScopeFields) == 0 {
		prepared.ScopeFields = []string{"scope_id"}
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
) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage)

	if profile.CleanupBehavior == RepairCleanupSourceFields {
		for _, field := range profile.SourceFields {
			if _, ok := view.Props[field]; ok {
				props[field] = json.RawMessage(nullPropertyValue)
			}
		}
	}

	return props
}
