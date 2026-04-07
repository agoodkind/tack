package service

import (
	"context"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/google/uuid"
)

type IssueService struct {
	issues   issue.Repository
	projects project.Repository
}

func NewIssueService(issues issue.Repository, projects project.Repository) *IssueService {
	return &IssueService{issues: issues, projects: projects}
}

func (s *IssueService) Create(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	seq, err := s.projects.AllocateSequenceID(ctx, i.ProjectID, "issue")
	if err != nil {
		return nil, fmt.Errorf("allocate sequence: %w", err)
	}
	i.SequenceID = seq
	return s.issues.Create(ctx, i)
}

func (s *IssueService) Get(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	return s.issues.GetByID(ctx, workspaceID, projectID, id)
}

func (s *IssueService) List(ctx context.Context, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	return s.issues.List(ctx, filter)
}

func (s *IssueService) Update(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	return s.issues.Update(ctx, i)
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
