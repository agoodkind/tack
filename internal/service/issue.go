package service

import (
	"context"
	"fmt"
	"time"

	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/google/uuid"
)

type IssueService struct {
	issues     issue.Repository
	projects   project.Repository
	workspaces workspace.Repository
	activity   node.ActivityRepository
}

func NewIssueService(
	issues issue.Repository,
	projects project.Repository,
	workspaces workspace.Repository,
	activity node.ActivityRepository,
) *IssueService {
	return &IssueService{
		issues:     issues,
		projects:   projects,
		workspaces: workspaces,
		activity:   activity,
	}
}

func (s *IssueService) Create(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, i.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	seq, err := s.projects.AllocateSequenceID(ctx, i.ProjectID, "issue")
	if err != nil {
		return nil, fmt.Errorf("allocate sequence: %w", err)
	}
	i.SequenceID = seq

	created, err := s.issues.Create(ctx, i)
	if err != nil {
		return nil, err
	}

	_ = s.activity.Append(ctx, ws.OrgID, created.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    created.NodeID,
		Actor:     created.CreatedBy,
		Verb:      "created",
		Detail:    map[string]any{"name": created.Name},
		CreatedAt: time.Now().UTC(),
	})

	return created, nil
}

func (s *IssueService) Get(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	return s.issues.GetByID(ctx, workspaceID, projectID, id)
}

func (s *IssueService) List(ctx context.Context, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	return s.issues.List(ctx, filter)
}

func (s *IssueService) Update(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, i.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	updated, err := s.issues.Update(ctx, i)
	if err != nil {
		return nil, err
	}

	_ = s.activity.Append(ctx, ws.OrgID, updated.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    updated.NodeID,
		Actor:     updated.CreatedBy,
		Verb:      "updated",
		Detail:    map[string]any{"name": updated.Name},
		CreatedAt: time.Now().UTC(),
	})

	return updated, nil
}

func (s *IssueService) Delete(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	_, err := s.issues.GetByID(ctx, workspaceID, projectID, id)
	if err != nil {
		return err
	}
	return s.issues.Delete(ctx, id)
}

func (s *IssueService) SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error {
	return s.issues.SetEpic(ctx, issueID, epicID)
}

func (s *IssueService) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	return s.issues.Search(ctx, workspaceID, q, filter)
}
