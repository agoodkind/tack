// Package service implements business logic for all Tack entities.
// It coordinates SQL repositories and FoundationDB stores, using errgroup
// for concurrent multi-source reads.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// IssueService implements issue.Service, coordinating SQL, FDB, and Meilisearch.
type IssueService struct {
	issues      issue.Repository
	projects    project.Repository
	workspaces  workspace.Repository
	activity    node.ActivityRepository
	assignments node.AssignmentRepository
	labels      node.NodeLabelRepository
	nodeDeleter node.NodeDeleter
	nodeCleanup node.NodeCleanupScheduler
	searcher    domainsearch.Searcher
}

func NewIssueService(
	issues issue.Repository,
	projects project.Repository,
	workspaces workspace.Repository,
	activity node.ActivityRepository,
	assignments node.AssignmentRepository,
	labels node.NodeLabelRepository,
	nodeDeleter node.NodeDeleter,
	nodeCleanup node.NodeCleanupScheduler,
	searcher domainsearch.Searcher,
) *IssueService {
	return &IssueService{
		issues:      issues,
		projects:    projects,
		workspaces:  workspaces,
		activity:    activity,
		assignments: assignments,
		labels:      labels,
		nodeDeleter: nodeDeleter,
		nodeCleanup: nodeCleanup,
		searcher:    searcher,
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

	// Write FDB relationships concurrently.
	if len(i.AssigneeIDs) > 0 {
		_ = s.assignments.SetAll(ctx, ws.OrgID, created.NodeID, i.AssigneeIDs, created.CreatedBy)
	}
	if len(i.LabelIDs) > 0 {
		_ = s.labels.SetAll(ctx, ws.OrgID, created.NodeID, i.LabelIDs, created.CreatedBy)
	}

	_ = s.activity.Append(ctx, ws.OrgID, created.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    created.NodeID,
		Actor:     created.CreatedBy,
		Verb:      "created",
		Detail:    map[string]any{"name": created.Name},
		CreatedAt: time.Now().UTC(),
	})

	created.AssigneeIDs = i.AssigneeIDs
	created.LabelIDs = i.LabelIDs

	_ = s.searcher.Index(ctx, "issues", created.ID.String(), issueDoc(created))

	telemetry.L(ctx).Info("issue.created",
		slog.String("issue_id", created.ID.String()),
		slog.String("project_id", created.ProjectID.String()),
		slog.String("workspace_id", created.WorkspaceID.String()),
		slog.Int("sequence_id", created.SequenceID),
	)
	return created, nil
}

func (s *IssueService) Get(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	// SQL fetch first to get node_id, then FDB concurrently.
	i, err := s.issues.GetByID(ctx, workspaceID, projectID, id)
	if err != nil {
		return nil, err
	}

	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return i, nil // non-fatal: return issue without FDB data
	}

	g, gCtx := errgroup.WithContext(ctx)
	var assignees, labels []uuid.UUID

	g.Go(func() error {
		var err error
		assignees, err = s.assignments.ListByNode(gCtx, ws.OrgID, i.NodeID)
		return err
	})
	g.Go(func() error {
		var err error
		labels, err = s.labels.ListByNode(gCtx, ws.OrgID, i.NodeID)
		return err
	})

	if err := g.Wait(); err == nil {
		i.AssigneeIDs = assignees
		i.LabelIDs = labels
	}

	return i, nil
}

func (s *IssueService) List(ctx context.Context, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	// List omits assignees/labels — too expensive to do N FDB reads per row.
	// Use Get for detail views where assignees are needed.
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

	// Use i.NodeID (from the input, populated by Get) — Update only returns updated_at.
	if i.AssigneeIDs != nil {
		_ = s.assignments.SetAll(ctx, ws.OrgID, i.NodeID, i.AssigneeIDs, i.CreatedBy)
	}
	if i.LabelIDs != nil {
		_ = s.labels.SetAll(ctx, ws.OrgID, i.NodeID, i.LabelIDs, i.CreatedBy)
	}

	_ = s.activity.Append(ctx, ws.OrgID, i.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    i.NodeID,
		Actor:     i.CreatedBy,
		Verb:      "updated",
		Detail:    map[string]any{"name": updated.Name},
		CreatedAt: time.Now().UTC(),
	})

	// Carry forward fields not returned by the SQL UPDATE clause.
	updated.NodeID = i.NodeID
	updated.AssigneeIDs = i.AssigneeIDs
	updated.LabelIDs = i.LabelIDs

	_ = s.searcher.Index(ctx, "issues", updated.ID.String(), issueDoc(updated))

	return updated, nil
}

func (s *IssueService) Delete(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	i, err := s.issues.GetByID(ctx, workspaceID, projectID, id)
	if err != nil {
		return err
	}
	if err := s.issues.Delete(ctx, id); err != nil {
		return err
	}
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err == nil {
		_ = s.nodeDeleter.DeleteNode(ctx, ws.OrgID, i.NodeID)
	}

	_ = s.searcher.Delete(ctx, "issues", id.String())

	telemetry.L(ctx).Info("issue.deleted",
		slog.String("issue_id", id.String()),
	)
	return nil
}

func (s *IssueService) SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error {
	return s.issues.SetEpic(ctx, issueID, epicID)
}
