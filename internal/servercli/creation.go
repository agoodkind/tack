package servercli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// createSeedNode writes the node id of typeKey with slug and name under
// parentID, its view, its slug index entry, and its child_of edge in one
// CreateAtomic transaction.
func createSeedNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	nodeID uuid.UUID,
	typeKey string,
	slug string,
	name string,
	parentID uuid.UUID,
) error {
	slugRaw, err := encodeSeedSlug(ctx, slug)
	if err != nil {
		return err
	}

	now := clock.Now().UTC()
	props := make(map[string]json.RawMessage)
	props["slug"] = slugRaw
	if parentID != uuid.Nil {
		parentRaw, marshalErr := json.Marshal(parentID.String())
		if marshalErr != nil {
			slog.ErrorContext(
				ctx,
				"seed.encode_parent_failed",
				slog.String("err", marshalErr.Error()),
			)

			return fmt.Errorf("seed: encode parent_id: %w", marshalErr)
		}

		props["parent_id"] = parentRaw
	}

	// A root node uses its own ID as OrgID. Other nodes use the parent ID
	// supplied for this seed hierarchy, independently of the node type name.
	orgID := parentID
	if parentID == uuid.Nil {
		orgID = nodeID
	}

	record := &node.Node{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
	rels := seedParentRelationships(orgID, nodeID, parentID, now)

	// Index slug for ListByProperty lookups through node_by_property.
	if err := stores.Nodes.CreateAtomic(
		ctx,
		record,
		view,
		rels,
		[]string{"slug"},
		nil,
		nil,
	); err != nil {
		slog.ErrorContext(
			ctx,
			"seed.create_node_failed",
			slog.String("type", typeKey),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("seed: create node of type %s: %w", typeKey, err)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"seed.node_created",
		"type",
		typeKey,
		"slug",
		slug,
		"id",
		nodeID,
	)

	return nil
}

func seedParentRelationships(
	orgID uuid.UUID,
	nodeID uuid.UUID,
	parentID uuid.UUID,
	now time.Time,
) []*node.Relationship {
	var rels []*node.Relationship
	if parentID != uuid.Nil {
		rels = append(
			rels,
			&node.Relationship{
				OrgID:        orgID,
				SourceID:     nodeID,
				RelationType: node.RelChildOf,
				TargetID:     parentID,
				CreatedBy:    uuid.Nil,
				Props:        nil,
				CreatedAt:    now,
			},
		)
	}

	return rels
}
