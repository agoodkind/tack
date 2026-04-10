package service

import (
	"context"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/cycle"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// CycleService wraps the cycle repository and adds search indexing on writes.
type CycleService struct {
	cycles   cycle.Repository
	searcher domainsearch.Searcher
}

// NewCycleService creates a CycleService with the given repository and searcher.
func NewCycleService(cycles cycle.Repository, searcher domainsearch.Searcher) *CycleService {
	return &CycleService{cycles: cycles, searcher: searcher}
}

func cycleDoc(c *cycle.Cycle) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          c.ID.String(),
		NodeID:      c.NodeID.String(),
		WorkspaceID: c.WorkspaceID.String(),
		ProjectID:   c.ProjectID.String(),
		EntityType:  "cycle",
		Name:        c.Name,
		Description: c.Description,
	}
}

func (s *CycleService) Create(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	created, err := s.cycles.Create(ctx, c)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", created.ID.String(), cycleDoc(created))
	telemetry.L(ctx).Info("cycle.created",
		slog.String("cycle_id", created.ID.String()),
		slog.String("project_id", created.ProjectID.String()),
	)
	return created, nil
}

func (s *CycleService) GetByID(ctx context.Context, projectID, id uuid.UUID) (*cycle.Cycle, error) {
	return s.cycles.GetByID(ctx, projectID, id)
}

func (s *CycleService) List(ctx context.Context, projectID uuid.UUID) ([]*cycle.Cycle, error) {
	return s.cycles.List(ctx, projectID)
}

func (s *CycleService) Update(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	updated, err := s.cycles.Update(ctx, c)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", updated.ID.String(), cycleDoc(updated))
	return updated, nil
}

func (s *CycleService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.cycles.Delete(ctx, id); err != nil {
		return err
	}
	_ = s.searcher.Delete(ctx, "nodes", id.String())
	telemetry.L(ctx).Info("cycle.deleted",
		slog.String("cycle_id", id.String()),
	)
	return nil
}
