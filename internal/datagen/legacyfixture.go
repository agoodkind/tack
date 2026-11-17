package datagen

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func (b *repairFixtureBuilder) addChildOf(ctx context.Context, sourceID, targetID uuid.UUID) {
	if b.err != nil {
		return
	}

	relationship := &node.Relationship{
		OrgID:        b.orgID,
		SourceID:     sourceID,
		RelationType: node.RelChildOf,
		TargetID:     targetID,
		CreatedBy:    b.actorID,
		CreatedAt:    clock.Now().UTC(),
		Props:        nil,
	}
	if err := b.stores.Relationships.Add(ctx, relationship); err != nil {
		b.err = loggedError(
			ctx,
			"qa datagen: add the legacy child_of edge from "+sourceID.String(),
			err,
		)
	}
}

// removeChildOf creates the missing-edge repair fixture through the low-level
// atomic writer. Strict validation rejects this fixture after activation.
func (b *repairFixtureBuilder) removeChildOf(ctx context.Context, sourceID, targetID uuid.UUID) {
	current, view := b.legacyFixtureRecords(ctx, sourceID)
	if b.err != nil {
		return
	}

	removed := &node.Relationship{
		OrgID:        b.orgID,
		SourceID:     sourceID,
		RelationType: node.RelChildOf,
		TargetID:     targetID,
		CreatedBy:    uuid.Nil,
		CreatedAt:    current.CreatedAt,
		Props:        nil,
	}
	err := b.stores.Nodes.UpdateAtomic(
		ctx,
		current,
		view,
		current.Props,
		nil,
		nil,
		node.RelationshipChanges{Add: nil, Remove: []*node.Relationship{removed}},
	)
	if err != nil {
		b.err = loggedError(
			ctx,
			"qa datagen: remove the legacy child_of edge from "+sourceID.String(),
			err,
		)
	}
}

// setLegacyProps writes invalid property values for the reviewed repair fixture
// before strict validation activates. It preserves all other record values.
func (b *repairFixtureBuilder) setLegacyProps(
	ctx context.Context,
	nodeID uuid.UUID,
	props map[string]json.RawMessage,
) {
	current, view := b.legacyFixtureRecords(ctx, nodeID)
	if b.err != nil {
		return
	}

	previous := maps.Clone(current.Props)
	maps.Copy(current.Props, props)
	view.Props = maps.Clone(current.Props)
	if err := b.stores.Nodes.UpdateAtomic(ctx, current, view, previous, nil, nil); err != nil {
		b.err = loggedError(ctx, "qa datagen: write legacy properties of "+nodeID.String(), err)
	}
}

func (b *repairFixtureBuilder) legacyFixtureRecords(ctx context.Context, nodeID uuid.UUID) (
	*node.Node,
	*node.NodeView,
) {
	if b.err != nil {
		return nil, nil
	}

	current, err := b.stores.Nodes.Get(ctx, b.orgID, nodeID)
	if err != nil || current == nil {
		b.err = loggedError(
			ctx,
			"qa datagen: read repair fixture node "+nodeID.String(),
			errMissing(err),
		)
		return nil, nil
	}

	view, err := b.stores.Views.Get(ctx, nodeID)
	if err != nil || view == nil {
		b.err = loggedError(
			ctx,
			"qa datagen: read repair fixture view "+nodeID.String(),
			errMissing(err),
		)
		return nil, nil
	}

	return current, view
}
