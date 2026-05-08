package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func prepareReferenceProfile(ctx context.Context, profile *RepairReferenceProfile) (RepairReferenceProfile, error) {
	if profile == nil {
		return RepairReferenceProfile{}, loggedRepairError(ctx, "reference repair profile required", domain.ErrInvalidArgument)
	}
	prepared := *profile
	prepared.TargetProperty = strings.TrimSpace(prepared.TargetProperty)
	if prepared.TargetProperty == "" {
		return RepairReferenceProfile{}, loggedRepairError(ctx, "reference repair target_property required", domain.ErrInvalidArgument)
	}
	prepared.SourceFields = compactStrings(prepared.SourceFields)
	if len(prepared.SourceFields) == 0 {
		return RepairReferenceProfile{}, loggedRepairError(ctx, "reference repair source_fields required", domain.ErrInvalidArgument)
	}
	prepared.ScopeFields = compactStrings(prepared.ScopeFields)
	if len(prepared.ScopeFields) == 0 {
		prepared.ScopeFields = []string{"scope_id", "parent_id"}
	}
	if prepared.CleanupBehavior == "" {
		prepared.CleanupBehavior = RepairCleanupSourceFields
	}
	if prepared.ConflictPolicy == "" {
		prepared.ConflictPolicy = RepairConflictPreferSource
	}
	return prepared, nil
}

func newReferencePreview(view *node.NodeView, profile RepairReferenceProfile, targetType *node.NodeType) *RepairPreview {
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

func observedReferenceSource(view *node.NodeView, field string, normalization RepairNormalization) RepairObservedSource {
	raw, present := view.Props[field]
	source := RepairObservedSource{Field: field, Raw: raw, Normalized: "", Present: present}
	if !present || len(raw) == 0 || string(raw) == "null" {
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

func chooseReferenceCandidate(candidates []RepairReferenceCandidate, policy RepairConflictPolicy) (*RepairReferenceCandidate, string) {
	resolved := resolvedReferenceCandidates(candidates)
	if len(resolved) == 0 {
		return nil, "no reference candidate resolved"
	}
	if policy == RepairConflictFail && distinctReferenceCandidateCount(resolved) > 1 {
		return nil, "reference candidates conflict"
	}
	sortReferenceCandidates(resolved, policy)
	return &resolved[0], ""
}

func plannedReferenceProps(view *node.NodeView, profile RepairReferenceProfile, chosenID uuid.UUID) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage)
	if rawUUIDProp(view.Props, profile.TargetProperty) != chosenID {
		props[profile.TargetProperty] = service.MustRawString(chosenID.String())
	}
	if profile.CleanupBehavior == RepairCleanupSourceFields {
		for _, field := range profile.SourceFields {
			if _, ok := view.Props[field]; ok {
				props[field] = json.RawMessage("null")
			}
		}
	}
	return props
}

func summarizeReferencePreview(preview *RepairPreview) string {
	if preview == nil {
		return "preview is unavailable"
	}
	if preview.ChosenCandidate == nil {
		return "no reference candidate was selected"
	}
	if !preview.NeedsRepair {
		return "reference property already matches selected candidate"
	}
	return fmt.Sprintf("set %s=%s from %s", preview.TargetProperty, preview.ChosenCandidate.NodeID, preview.ChosenCandidate.SourceField)
}

func repairConfirmationToken(preview *RepairPreview) string {
	plannedProps, err := json.Marshal(preview.PlannedProps)
	if err != nil {
		plannedProps = []byte("null")
	}
	plannedRelationships, err := json.Marshal(preview.PlannedRelationships)
	if err != nil {
		plannedRelationships = []byte("null")
	}
	payload := strings.Join([]string{
		string(preview.Class),
		preview.ProfileName,
		preview.NodeID.String(),
		preview.CurrentUpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		preview.TargetProperty,
		string(plannedProps),
		string(plannedRelationships),
	}, "|")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func repairScopeID(view *node.NodeView, fields []string) uuid.UUID {
	for _, field := range fields {
		if id := rawUUIDProp(view.Props, field); id != uuid.Nil {
			return id
		}
	}
	return uuid.Nil
}

func referenceSourcesPresent(sources []RepairObservedSource) bool {
	for _, source := range sources {
		if source.Present {
			return true
		}
	}
	return false
}

func resolvedReferenceCandidates(candidates []RepairReferenceCandidate) []RepairReferenceCandidate {
	resolved := make([]RepairReferenceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Status == "resolved" && candidate.NodeID != uuid.Nil {
			resolved = append(resolved, candidate)
		}
	}
	return resolved
}
