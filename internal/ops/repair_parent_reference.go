package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
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
	parents, err := c.reader.List(ctx, repairParentQuery(view.OrgID, view.ID))
	if err != nil {
		return nil, loggedRepairError(
			ctx,
			fmt.Sprintf("list parents of repair node %s", view.ID),
			err,
		)
	}

	includeExisting := len(resolvedReferenceCandidates(candidates)) == 0 ||
		preparedProfile.ConflictPolicy != RepairConflictPreferSource
	if len(parents) == 1 && includeExisting {
		candidates = append(
			candidates,
			matchContext.candidate(ctx, "__existing__", parents[0].ID.String()),
		)
		preview.Candidates = candidates
	}

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
