package service

import (
	"context"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// WorkspaceService wraps the workspace repository and adds search indexing on writes.
type WorkspaceService struct {
	workspaces workspace.Repository
	searcher   domainsearch.Searcher
}

// NewWorkspaceService creates a WorkspaceService with the given repository and searcher.
func NewWorkspaceService(workspaces workspace.Repository, searcher domainsearch.Searcher) *WorkspaceService {
	return &WorkspaceService{workspaces: workspaces, searcher: searcher}
}

func workspaceDoc(w *workspace.Workspace) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          w.ID.String(),
		NodeID:      w.NodeID.String(),
		WorkspaceID: w.ID.String(),
		EntityType:  "workspace",
		Name:        w.Name,
	}
}

func (s *WorkspaceService) Create(ctx context.Context, w *workspace.Workspace) (*workspace.Workspace, error) {
	created, err := s.workspaces.Create(ctx, w)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", created.ID.String(), workspaceDoc(created))
	telemetry.L(ctx).Info("workspace.created",
		slog.String("workspace_id", created.ID.String()),
	)
	return created, nil
}

func (s *WorkspaceService) GetByID(ctx context.Context, id uuid.UUID) (*workspace.Workspace, error) {
	return s.workspaces.GetByID(ctx, id)
}

func (s *WorkspaceService) GetBySlug(ctx context.Context, slug string) (*workspace.Workspace, error) {
	return s.workspaces.GetBySlug(ctx, slug)
}

func (s *WorkspaceService) List(ctx context.Context, orgID uuid.UUID) ([]*workspace.Workspace, error) {
	return s.workspaces.List(ctx, orgID)
}

func (s *WorkspaceService) ListForUser(ctx context.Context, userID uuid.UUID) ([]*workspace.Workspace, error) {
	return s.workspaces.ListForUser(ctx, userID)
}
