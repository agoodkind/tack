package service

import (
	"context"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/module"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// ModuleService wraps the module repository and adds search indexing on writes.
type ModuleService struct {
	modules  module.Repository
	searcher domainsearch.Searcher
}

// NewModuleService creates a ModuleService with the given repository and searcher.
func NewModuleService(modules module.Repository, searcher domainsearch.Searcher) *ModuleService {
	return &ModuleService{modules: modules, searcher: searcher}
}

func moduleDoc(m *module.Module) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          m.ID.String(),
		NodeID:      m.NodeID.String(),
		WorkspaceID: m.WorkspaceID.String(),
		ProjectID:   m.ProjectID.String(),
		EntityType:  "module",
		Name:        m.Name,
		Description: m.Description,
	}
}

func (s *ModuleService) Create(ctx context.Context, m *module.Module) (*module.Module, error) {
	created, err := s.modules.Create(ctx, m)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", created.ID.String(), moduleDoc(created))
	telemetry.L(ctx).Info("module.created",
		slog.String("module_id", created.ID.String()),
		slog.String("project_id", created.ProjectID.String()),
	)
	return created, nil
}

func (s *ModuleService) GetByID(ctx context.Context, projectID, id uuid.UUID) (*module.Module, error) {
	return s.modules.GetByID(ctx, projectID, id)
}

func (s *ModuleService) List(ctx context.Context, projectID uuid.UUID) ([]*module.Module, error) {
	return s.modules.List(ctx, projectID)
}

func (s *ModuleService) Update(ctx context.Context, m *module.Module) (*module.Module, error) {
	updated, err := s.modules.Update(ctx, m)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", updated.ID.String(), moduleDoc(updated))
	return updated, nil
}

func (s *ModuleService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.modules.Delete(ctx, id); err != nil {
		return err
	}
	_ = s.searcher.Delete(ctx, "nodes", id.String())
	telemetry.L(ctx).Info("module.deleted",
		slog.String("module_id", id.String()),
	)
	return nil
}
