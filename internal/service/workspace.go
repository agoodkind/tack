package service

import (
	"context"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// WorkspaceService wraps the workspace repository and adds search indexing on writes.
type WorkspaceService struct {
	workspaces workspace.Repository
	seeder     *WorkspaceSeeder
	searcher   domainsearch.Searcher
	nodeTypes  node.TypeRepository
}

// NewWorkspaceService creates a WorkspaceService with the given repository and searcher.
func NewWorkspaceService(workspaces workspace.Repository, seeder *WorkspaceSeeder, searcher domainsearch.Searcher, nodeTypes node.TypeRepository) *WorkspaceService {
	return &WorkspaceService{workspaces: workspaces, seeder: seeder, searcher: searcher, nodeTypes: nodeTypes}
}

func workspaceDoc(w *workspace.Workspace) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          w.ID.String(),
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
	// Seed default property defs (priority, due_date, start_date, is_draft).
	s.seeder.SeedWorkspace(ctx, created.OrgID, created.ID)
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

// Describe returns the workspace (looked up by slug) and all node types seeded for its org.
func (s *WorkspaceService) Describe(ctx context.Context, slug string) (*workspace.Workspace, []*node.NodeType, error) {
	ws, err := s.workspaces.GetBySlug(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	types, err := s.nodeTypes.List(ctx, ws.OrgID)
	if err != nil {
		return nil, nil, err
	}
	return ws, types, nil
}
