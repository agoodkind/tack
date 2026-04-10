package service

import (
	"context"
	"log/slog"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/epic"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// EpicService wraps the epic repository and adds search indexing on writes.
type EpicService struct {
	epics    epic.Repository
	searcher domainsearch.Searcher
}

// NewEpicService creates an EpicService with the given repository and searcher.
func NewEpicService(epics epic.Repository, searcher domainsearch.Searcher) *EpicService {
	return &EpicService{epics: epics, searcher: searcher}
}

func epicDoc(e *epic.Epic) domainsearch.NodeDoc {
	return domainsearch.NodeDoc{
		ID:          e.ID.String(),
		NodeID:      e.NodeID.String(),
		WorkspaceID: e.WorkspaceID.String(),
		ProjectID:   e.ProjectID.String(),
		EntityType:  "epic",
		Name:        e.Name,
		Description: e.Description,
	}
}

func (s *EpicService) Create(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	created, err := s.epics.Create(ctx, e)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", created.ID.String(), epicDoc(created))
	telemetry.L(ctx).Info("epic.created",
		slog.String("epic_id", created.ID.String()),
		slog.String("project_id", created.ProjectID.String()),
	)
	return created, nil
}

func (s *EpicService) GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*epic.Epic, error) {
	return s.epics.GetByID(ctx, workspaceID, projectID, id)
}

func (s *EpicService) List(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*epic.Epic, int, error) {
	return s.epics.List(ctx, workspaceID, projectID)
}

func (s *EpicService) Update(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	updated, err := s.epics.Update(ctx, e)
	if err != nil {
		return nil, err
	}
	_ = s.searcher.Index(ctx, "nodes", updated.ID.String(), epicDoc(updated))
	return updated, nil
}

func (s *EpicService) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.epics.Delete(ctx, id); err != nil {
		return err
	}
	_ = s.searcher.Delete(ctx, "nodes", id.String())
	telemetry.L(ctx).Info("epic.deleted",
		slog.String("epic_id", id.String()),
	)
	return nil
}

func (s *EpicService) ListIssueIDs(ctx context.Context, epicID uuid.UUID) ([]uuid.UUID, error) {
	return s.epics.ListIssueIDs(ctx, epicID)
}
