package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func (c *RepairConsole) planReferenceProperty(
	ctx context.Context,
	nodeID uuid.UUID,
	profile *RepairReferenceProfile,
) (*repairPlan, error) {
	preparedProfile, err := prepareReferenceProfile(ctx, profile)
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

	metadata, err := c.loadReferenceRepairMetadata(ctx, view, preparedProfile, false)
	if err != nil {
		return nil, err
	}

	preview := newReferencePreview(view, preparedProfile, metadata.targetType)
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
		metadata.targetDef,
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
	props := plannedReferenceProps(view, preparedProfile, chosen.NodeID)
	preview.PlannedProps = props
	preview.NeedsRepair = len(props) > 0
	preview.CanApply = preview.NeedsRepair
	preview.Summary = summarizeReferencePreview(preview)
	if preview.CanApply {
		preview.ConfirmationToken = repairConfirmationToken(preview)
	}

	return &repairPlan{
		preview:             preview,
		props:               props,
		relationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
	}, nil
}
