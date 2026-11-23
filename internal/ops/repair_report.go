package ops

import (
	"context"
	"strings"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func prepareReferenceProfile(
	ctx context.Context,
	profile *RepairReferenceProfile,
) (RepairReferenceProfile, error) {
	if profile == nil {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"reference repair profile required",
			domain.ErrInvalidArgument,
		)
	}

	prepared := *profile
	prepared.TargetProperty = strings.TrimSpace(prepared.TargetProperty)
	if prepared.TargetProperty == "" {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"reference repair target_property required",
			domain.ErrInvalidArgument,
		)
	}

	prepared.SourceFields = compactStrings(prepared.SourceFields)
	if len(prepared.SourceFields) == 0 {
		return RepairReferenceProfile{}, loggedRepairError(
			ctx,
			"reference repair source_fields required",
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

func newReferencePreview(
	view *node.NodeView,
	profile RepairReferenceProfile,
	targetType *node.NodeType,
) *RepairPreview {
	return &RepairPreview{
		Class:                RepairClassReferenceProperty,
		ProfileName:          profile.Name,
		NodeID:               view.ID,
		NodeType:             view.NodeType,
		CurrentUpdatedAt:     view.UpdatedAt,
		TargetProperty:       profile.TargetProperty,
		TargetType:           nodeTypeKey(targetType),
		ObservedSources:      make([]RepairObservedSource, 0, len(profile.SourceFields)),
		Candidates:           nil,
		ChosenCandidate:      nil,
		PlannedProps:         nil,
		PlannedRelationships: nil,
		Summary:              "",
		ConfirmationToken:    "",
		NeedsRepair:          false,
		CanApply:             false,
	}
}

func observedReferenceSource(
	view *node.NodeView,
	field string,
	normalization RepairNormalization,
) RepairObservedSource {
	raw, present := view.Props[field]
	source := RepairObservedSource{Field: field, Raw: raw, Normalized: "", Present: present}
	if !present || len(raw) == 0 || string(raw) == nullPropertyValue {
		return source
	}

	if value := rawStringValue(raw); value != "" {
		source.Normalized = normalizeReferenceInput(value, normalization)
	}

	return source
}

func normalizeReferenceInput(value string, normalization RepairNormalization) string {
	normalized := value
	if normalization.Trim {
		normalized = strings.TrimSpace(normalized)
	}

	lookup := normalized
	if normalization.CaseFold {
		lookup = strings.ToLower(lookup)
	}

	if mapped, ok := normalization.ValueMap[lookup]; ok {
		normalized = mapped
	}

	return normalized
}

func referenceSourcesPresent(sources []RepairObservedSource) bool {
	for _, source := range sources {
		if source.Present {
			return true
		}
	}

	return false
}
