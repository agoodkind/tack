package datagen

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func (binding *repairFixtureBuilder) addChildOf(ctx context.Context, sourceID, targetID uuid.UUID) {
	if binding.err != nil {
		return
	}
	relationship := &node.Relationship{
		OrgID:        binding.orgID,
		SourceID:     sourceID,
		RelationType: node.RelChildOf,
		TargetID:     targetID,
		CreatedBy:    binding.actorID,
		CreatedAt:    clock.Now().UTC(),
		Props:        nil,
	}
	if err := binding.stores.Relationships.Add(ctx, relationship); err != nil {
		binding.err = loggedError(
			ctx,
			"qa datagen: add the legacy child_of edge from "+sourceID.String(),
			err,
		)
	}
}

// removeChildOf removes the child_of edge from sourceID to targetID through
// the relationship store without changing parent_id.
func (binding *repairFixtureBuilder) removeChildOf(
	ctx context.Context,
	sourceID uuid.UUID,
	targetID uuid.UUID,
) {
	if binding.err != nil {
		return
	}
	if err := binding.stores.Relationships.Remove(
		ctx,
		binding.orgID,
		sourceID,
		node.RelChildOf,
		targetID,
	); err != nil {
		binding.err = loggedError(
			ctx,
			"qa datagen: remove the child_of edge from "+sourceID.String(),
			err,
		)
	}
}

// setLegacyProps reads the stored primary record and view for nodeID.
// It sets each property in props on both records and preserves every other
// stored property. It writes both records through NodeStore.UpdateAtomic
// without validation.
func (binding *repairFixtureBuilder) setLegacyProps(
	ctx context.Context,
	nodeID uuid.UUID,
	props map[string]json.RawMessage,
) {
	if binding.err != nil {
		return
	}
	current, err := binding.stores.Nodes.Get(ctx, binding.orgID, nodeID)
	if err != nil || current == nil {
		binding.err = loggedError(
			ctx,
			"qa datagen: read repair fixture node "+nodeID.String(),
			errMissing(err),
		)
		return
	}
	view, err := binding.stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		binding.err = loggedError(
			ctx,
			"qa datagen: read repair fixture view "+nodeID.String(),
			errMissing(err),
		)
		return
	}
	previous := maps.Clone(current.Props)
	maps.Copy(current.Props, props)
	view.Props = maps.Clone(current.Props)
	if err := binding.stores.Nodes.UpdateAtomic(
		ctx,
		current,
		view,
		previous,
		nil,
		nil,
	); err != nil {
		binding.err = loggedError(
			ctx,
			"qa datagen: write legacy properties of "+nodeID.String(),
			err,
		)
	}
}
