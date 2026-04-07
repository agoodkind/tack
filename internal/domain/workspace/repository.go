package workspace

import "context"

type Repository interface {
	GetBySlug(ctx context.Context, slug string) (*Workspace, error)
	List(ctx context.Context) ([]*Workspace, error)
}
