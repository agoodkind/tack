package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type resolverReader struct {
	views      map[uuid.UUID]*node.NodeView
	workspaces []*node.NodeView
	parents    map[uuid.UUID]uuid.UUID
}

func (r *resolverReader) Get(_ context.Context, id uuid.UUID) (*node.NodeView, error) {
	return r.views[id], nil
}

func (r *resolverReader) Resolve(_ context.Context, id uuid.UUID) (*node.NodeResolve, error) {
	view := r.views[id]
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return &node.NodeResolve{OrgID: view.OrgID, NodeType: view.NodeType}, nil
}

func (r *resolverReader) List(_ context.Context, q node.NodeListQuery) ([]*node.NodeView, error) {
	if q.BySourceRelation != nil || q.ByTargetRelation != nil {
		return parentViews(r.views, r.parents, q), nil
	}

	if q.NodeType == "workspace" && q.ByProperty == nil {
		return r.workspaces, nil
	}

	if q.ByProperty == nil {
		return nil, nil
	}

	out := make([]*node.NodeView, 0)

	for _, view := range r.views {
		if view.NodeType != q.NodeType {
			continue
		}

		if string(view.Props[q.ByProperty.PropName]) != string(q.ByProperty.Value) {
			continue
		}

		out = append(out, view)
	}

	return out, nil
}

func (r *resolverReader) Stream(
	context.Context,
	node.NodeListQuery,
) (<-chan node.NodeStreamResult, error) {
	panic("resolverReader.Stream called")
}

func (r *resolverReader) ListPage(ctx context.Context, q node.NodeListQuery) (node.Page, error) {
	views, err := r.List(ctx, q)
	if err != nil {
		return node.Page{}, err
	}

	if q.Limit > 0 && len(views) > q.Limit {
		views = views[:q.Limit]
	}

	return node.Page{Views: views, NextCursor: ""}, nil
}
