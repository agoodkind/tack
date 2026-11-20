package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func (c *RepairConsole) planParentReference(
	ctx context.Context,
	nodeID uuid.UUID,
	profile *RepairReferenceProfile,
) (*repairPlan, error) {
	preparedProfile, err := prepareParentReferenceProfile(ctx, profile)
	if err != nil {
		return nil, err
	}

	view, err := c.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("get repair node %s", nodeID), err)
	}

	if view == nil {
		return nil, domain.ErrNotFound
	}

	metadata, err := c.loadReferenceRepairMetadata(ctx, view, preparedProfile, true)
	if err != nil {
		return nil, err
	}

	preview := newReferencePreview(view, preparedProfile, metadata.targetType)
	preview.Class = RepairClassParentReference
	matchContext := referenceMatchContext{
		reader:        c.reader,
		typeIndex:     metadata.typeIndex,
		orgID:         view.OrgID,
		scopeID:       metadata.scopeID,
		targetType:    metadata.targetType,
		rankProperty:  preparedProfile.RankProperty,
		matchProperty: preparedProfile.TargetMatchProperty,
	}
	candidates := collectReferenceCandidates(
		ctx,
		view,
		preparedProfile,
		nil,
		matchContext,
		&preview.ObservedSources,
	)
	preview.Candidates = candidates
	chosen, blockReason := chooseReferenceCandidate(candidates, preparedProfile.ConflictPolicy)
	if blockReason != "" {
		preview.NeedsRepair = referenceSourcesPresent(preview.ObservedSources)
		preview.Summary = blockReason

		return &repairPlan{
			preview:             preview,
			props:               nil,
			relationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		}, nil
	}

	preview.ChosenCandidate = chosen
	props := plannedParentReferenceProps(view, preparedProfile, chosen.NodeID)
	changes, err := c.plannedParentRelationshipChanges(ctx, view, chosen.NodeID)
	if err != nil {
		return nil, err
	}

	preview.PlannedProps = props
	preview.PlannedRelationships = previewRelationshipChanges(changes)
	preview.NeedsRepair = len(props) > 0 || len(changes.Add) > 0 || len(changes.Remove) > 0
	preview.CanApply = preview.NeedsRepair
	preview.Summary = summarizeParentReferencePreview(preview)
	if preview.CanApply {
		preview.ConfirmationToken = repairConfirmationToken(preview)
	}

	return &repairPlan{preview: preview, props: props, relationshipChanges: changes}, nil
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

func relationshipTargetPlanned(relationships []*node.Relationship, targetID uuid.UUID) bool {
	for _, relationship := range relationships {
		if relationship.TargetID == targetID {
			return true
		}
	}

	return false
}
