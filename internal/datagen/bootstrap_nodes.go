package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

func bootstrapOrg(
	ctx context.Context,
	stores *fdbadapter.Stores,
	workspace *WorkspaceIdentity,
) error {
	return ensureBootstrapNode(ctx, stores, bootstrapNode{
		ID: workspace.OrgID, OrgID: workspace.OrgID, TypeKey: "org",
		Name: "QA Organization " + workspace.OrgSlug,
		Slug: workspace.OrgSlug, ParentID: uuid.Nil,
	})
}

func bootstrapWorkspace(
	ctx context.Context,
	stores *fdbadapter.Stores,
	workspace *WorkspaceIdentity,
) error {
	return ensureBootstrapNode(ctx, stores, bootstrapNode{
		ID:    node.WorkspaceID(workspace.OrgID, workspace.Slug),
		OrgID: workspace.OrgID, TypeKey: "workspace",
		Name: workspace.Name, Slug: workspace.Slug, ParentID: workspace.OrgID,
	})
}

type bootstrapNode struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	TypeKey  string
	Name     string
	Slug     string
	ParentID uuid.UUID
}

func ensureBootstrapNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	input bootstrapNode,
) error {
	existing, err := stores.Views.Get(ctx, input.ID)
	if err != nil {
		return loggedError(
			ctx,
			fmt.Sprintf("qa datagen: read bootstrap %s %s", input.TypeKey, input.ID),
			err,
		)
	}
	if existing != nil {
		return nil
	}
	props := map[string]json.RawMessage{"slug": json.RawMessage(strconv.Quote(input.Slug))}
	relationships := make([]*node.Relationship, 0, 1)
	if input.ParentID != uuid.Nil {
		props["parent_id"] = json.RawMessage(strconv.Quote(input.ParentID.String()))
		relationships = append(relationships, &node.Relationship{
			OrgID: input.OrgID, SourceID: input.ID, RelationType: node.RelChildOf,
			TargetID: input.ParentID, CreatedBy: uuid.Nil, Props: nil,
			CreatedAt: clock.Now().UTC(),
		})
	}
	now := clock.Now().UTC()
	value := &node.Node{
		ID: input.ID, OrgID: input.OrgID, NodeType: input.TypeKey,
		Name: input.Name, Props: props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
		CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: input.ID, OrgID: input.OrgID, NodeType: input.TypeKey,
		Name: input.Name, Props: props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := stores.Nodes.CreateAtomic(
		ctx,
		value,
		view,
		relationships,
		[]string{"slug"},
		nil,
		nil,
	); err != nil {
		return loggedError(
			ctx,
			fmt.Sprintf("qa datagen: create bootstrap %s %s", input.TypeKey, input.ID),
			err,
		)
	}
	return nil
}
