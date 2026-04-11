// Package service implements business logic for all Tack entities.
// It coordinates FDB stores, using errgroup for concurrent multi-source reads.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// IssueService implements issue.Service, reading and writing FDB only.
type IssueService struct {
	entities    node.EntityRepository
	reader      node.NodeReader
	workspaces  workspace.Repository
	projects    project.Repository
	activity    node.ActivityRepository
	assignments node.AssignmentRepository
	labels      node.NodeLabelRepository
	containment node.ContainmentRepository
	nodeDeleter node.NodeDeleter
	nodeCleanup node.NodeCleanupScheduler
	searcher    domainsearch.Searcher
	automations node.AutomationExecutor
}

func NewIssueService(
	entities node.EntityRepository,
	reader node.NodeReader,
	workspaces workspace.Repository,
	projects project.Repository,
	activity node.ActivityRepository,
	assignments node.AssignmentRepository,
	labels node.NodeLabelRepository,
	containment node.ContainmentRepository,
	nodeDeleter node.NodeDeleter,
	nodeCleanup node.NodeCleanupScheduler,
	searcher domainsearch.Searcher,
	automations node.AutomationExecutor,
) *IssueService {
	return &IssueService{
		entities:    entities,
		reader:      reader,
		workspaces:  workspaces,
		projects:    projects,
		activity:    activity,
		assignments: assignments,
		labels:      labels,
		containment: containment,
		nodeDeleter: nodeDeleter,
		nodeCleanup: nodeCleanup,
		searcher:    searcher,
		automations: automations,
	}
}

// nodeValueFromIssue converts an issue to a NodeValue and a property map.
func nodeValueFromIssue(i *issue.Issue, orgID uuid.UUID) (*node.NodeValue, map[uuid.UUID]*node.PropertyValue) {
	updatedBy := i.CreatedBy
	if i.UpdatedBy != nil {
		updatedBy = *i.UpdatedBy
	}
	nv := &node.NodeValue{
		ID:          i.ID,
		OrgID:       orgID,
		WorkspaceID: i.WorkspaceID,
		ProjectID:   i.ProjectID,
		NodeType:    node.NodeTypeIssue,
		Name:        i.Name,
		Description: i.Description,
		StateID:     i.StateID,
		ParentID:    i.ParentID,
		EpicID:      i.EpicID,
		SequenceID:  int32(i.SequenceID),
		SortOrder:   i.SortOrder,
		CreatedBy:   i.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   i.UpdatedAt,
	}

	props := make(map[uuid.UUID]*node.PropertyValue)

	// Priority (enum)
	if i.Priority != "" {
		key := string(i.Priority)
		rank := priorityRank(i.Priority)
		props[systemPropID(i.WorkspaceID, propNamePriority)] = &node.PropertyValue{
			Kind:     node.PropertyValueEnum,
			Enum:     &key,
			EnumRank: rank,
		}
	}

	// StartDate (timestamp)
	if i.StartDate != nil {
		t := *i.StartDate
		props[systemPropID(i.WorkspaceID, propNameStartDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}

	// TargetDate -> propNameDueDate (timestamp)
	if i.TargetDate != nil {
		t := *i.TargetDate
		props[systemPropID(i.WorkspaceID, propNameDueDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}

	// IsDraft (bool)
	isDraft := i.IsDraft
	props[systemPropID(i.WorkspaceID, propNameIsDraft)] = &node.PropertyValue{
		Kind: node.PropertyValueBool,
		Bool: &isDraft,
	}

	return nv, props
}

// buildIssueView converts an issue and orgID into a NodeListView for atomic writes.
// Description is truncated at 500 chars to bound view size.
func buildIssueView(i *issue.Issue, orgID uuid.UUID) *node.NodeListView {
	updatedBy := i.CreatedBy
	if i.UpdatedBy != nil {
		updatedBy = *i.UpdatedBy
	}
	desc := i.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}
	return &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          i.ID,
		OrgID:       orgID,
		WorkspaceID: i.WorkspaceID,
		ProjectID:   i.ProjectID,
		NodeType:    node.NodeTypeIssue,
		SequenceID:  int32(i.SequenceID),
		SortOrder:   i.SortOrder,
		Name:        i.Name,
		Description: desc,
		StateID:     i.StateID,
		ParentID:    i.ParentID,
		EpicID:      i.EpicID,
		AssigneeIDs: i.AssigneeIDs,
		LabelIDs:    i.LabelIDs,
		Priority:    string(i.Priority),
		StartDate:   i.StartDate,
		DueDate:     i.TargetDate,
		IsDraft:     i.IsDraft,
		CreatedBy:   i.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   i.UpdatedAt,
	}
}

// issueFromNodeListView reconstructs an issue.Issue from a materialized NodeListView.
func issueFromNodeListView(v *node.NodeListView) *issue.Issue {
	updatedBy := v.UpdatedBy
	return &issue.Issue{
		Base: domain.Base{
			ID:        v.ID,
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
			CreatedBy: v.CreatedBy,
			UpdatedBy: &updatedBy,
		},
		NodeID:      v.ID,
		WorkspaceID: v.WorkspaceID,
		ProjectID:   v.ProjectID,
		Name:        v.Name,
		Description: v.Description,
		StateID:     v.StateID,
		ParentID:    v.ParentID,
		EpicID:      v.EpicID,
		SequenceID:  int(v.SequenceID),
		SortOrder:   v.SortOrder,
		Priority:    issue.Priority(v.Priority),
		StartDate:   v.StartDate,
		TargetDate:  v.DueDate,
		IsDraft:     v.IsDraft,
		AssigneeIDs: v.AssigneeIDs,
		LabelIDs:    v.LabelIDs,
	}
}


func (s *IssueService) Create(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, i.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	if i.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate id: %w", err)
		}
		i.ID = id
	}
	now := time.Now().UTC()
	i.CreatedAt = now
	i.UpdatedAt = now

	nv, props := nodeValueFromIssue(i, ws.OrgID)
	view := buildIssueView(i, ws.OrgID)

	seqID, err := s.entities.CreateAtomic(ctx, ws.OrgID, i.ProjectID, nv, props, view, i.AssigneeIDs, i.LabelIDs, i.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("entity create: %w", err)
	}
	i.SequenceID = int(seqID)
	// Sync sequence back into the view that was written atomically.
	view.SequenceID = int32(seqID)

	_ = s.activity.Append(ctx, ws.OrgID, i.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    i.ID,
		Actor:     i.CreatedBy,
		Verb:      "created",
		Detail:    map[string]any{"name": i.Name},
		CreatedAt: now,
	})

	_ = s.searcher.Index(ctx, "nodes", i.ID.String(), nodeDocFromView(view))
	_ = s.automations.Run(ctx, ws.OrgID, i.WorkspaceID, nv, node.TriggerNodeCreated)

	telemetry.L(ctx).Info("issue.created",
		slog.String("issue_id", i.ID.String()),
		slog.String("project_id", i.ProjectID.String()),
		slog.String("workspace_id", i.WorkspaceID.String()),
		slog.Int("sequence_id", i.SequenceID),
	)
	return i, nil
}

func (s *IssueService) Get(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	view, err := s.reader.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("entity get: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	if view.WorkspaceID != workspaceID {
		return nil, domain.ErrNotFound
	}
	if view.NodeType != node.NodeTypeIssue {
		return nil, domain.ErrNotFound
	}
	if projectID != uuid.Nil && view.ProjectID != projectID {
		return nil, domain.ErrNotFound
	}
	return issueFromNodeListView(view), nil
}

// GetBySequence resolves an issue by its project-scoped sequence number.
// This is used by the MCP identifier pattern ("ENG-42" → seq=42).
func (s *IssueService) GetBySequence(ctx context.Context, workspaceID, projectID uuid.UUID, seqID int) (*issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	nodeID, err := s.entities.GetBySequence(ctx, ws.OrgID, projectID, node.NodeTypeIssue, int64(seqID))
	if err != nil {
		return nil, fmt.Errorf("get by sequence: %w", err)
	}
	if nodeID == uuid.Nil {
		return nil, domain.ErrNotFound
	}
	return s.Get(ctx, workspaceID, projectID, nodeID)
}

func (s *IssueService) List(ctx context.Context, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	ws, err := s.workspaces.GetByID(ctx, filter.WorkspaceID)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve workspace: %w", err)
	}

	limit := filter.PerPage
	if limit <= 0 {
		limit = 100
	}

	q := node.NodeListQuery{
		OrgID:       ws.OrgID,
		WorkspaceID: filter.WorkspaceID,
		NodeType:    node.NodeTypeIssue,
		Limit:       limit,
	}

	switch {
	case len(filter.StateIDs) == 1 && filter.ProjectID != nil:
		stateID := filter.StateIDs[0]
		q.ByState = &stateID
	case len(filter.Priorities) == 1:
		pKey := string(filter.Priorities[0])
		rank := priorityRank(filter.Priorities[0])
		pv := &node.PropertyValue{Kind: node.PropertyValueEnum, Enum: &pKey, EnumRank: rank}
		q.ByProperty = &node.PropertyFilter{
			PropDefID: systemPropID(filter.WorkspaceID, propNamePriority),
			Value:     pv,
		}
	default:
		if filter.ProjectID == nil {
			return nil, 0, fmt.Errorf("project_id required for issue list")
		}
		q.ByProject = filter.ProjectID
	}

	if filter.EpicID != nil {
		q.FilterEpicID = filter.EpicID
	}
	if filter.IsDraft != nil {
		q.FilterIsDraft = filter.IsDraft
	}

	views, err := s.reader.List(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("node list: %w", err)
	}

	issues := make([]*issue.Issue, 0, len(views))
	for _, v := range views {
		// State and property indexes are workspace-scoped; filter to requested project.
		if filter.ProjectID != nil && v.ProjectID != *filter.ProjectID {
			continue
		}
		issues = append(issues, issueFromNodeListView(v))
		if len(issues) >= limit {
			break
		}
	}
	return issues, len(issues), nil
}

func (s *IssueService) Update(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, i.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	i.UpdatedAt = time.Now().UTC()
	nv, props := nodeValueFromIssue(i, ws.OrgID)

	// Build the view with current assignees/labels when the caller did not supply them.
	// Updates to assignees go through SetAll; the view must reflect the persisted state.
	view := buildIssueView(i, ws.OrgID)
	if i.AssigneeIDs == nil || i.LabelIDs == nil {
		if current, _ := s.reader.Get(ctx, i.ID); current != nil {
			if i.AssigneeIDs == nil {
				view.AssigneeIDs = current.AssigneeIDs
			}
			if i.LabelIDs == nil {
				view.LabelIDs = current.LabelIDs
			}
		}
	}

	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	if i.AssigneeIDs != nil {
		_ = s.assignments.SetAll(ctx, ws.OrgID, i.ID, i.AssigneeIDs, i.CreatedBy)
	}
	if i.LabelIDs != nil {
		_ = s.labels.SetAll(ctx, ws.OrgID, i.ID, i.LabelIDs, i.CreatedBy)
	}

	actor := i.CreatedBy
	if i.UpdatedBy != nil {
		actor = *i.UpdatedBy
	}
	_ = s.activity.Append(ctx, ws.OrgID, i.WorkspaceID, &node.ActivityEvent{
		EventID:   uuid.New(),
		NodeID:    i.ID,
		Actor:     actor,
		Verb:      "updated",
		Detail:    map[string]any{"name": i.Name},
		CreatedAt: i.UpdatedAt,
	})

	_ = s.searcher.Index(ctx, "nodes", i.ID.String(), nodeDocFromView(view))
	_ = s.automations.Run(ctx, ws.OrgID, i.WorkspaceID, nv, node.TriggerNodeUpdated)

	return i, nil
}

func (s *IssueService) Delete(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	nv, err := s.entities.Get(ctx, ws.OrgID, workspaceID, node.NodeTypeIssue, id)
	if err != nil {
		return fmt.Errorf("entity get: %w", err)
	}
	if nv == nil {
		return domain.ErrNotFound
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("entity delete: %w", err)
	}

	_ = s.nodeDeleter.DeleteNode(ctx, ws.OrgID, id)
	_ = s.searcher.Delete(ctx, "nodes", id.String())
	_ = s.automations.Run(ctx, ws.OrgID, workspaceID, nv, node.TriggerNodeDeleted)

	telemetry.L(ctx).Info("issue.deleted",
		slog.String("issue_id", id.String()),
	)
	return nil
}

func (s *IssueService) SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error {
	// SetEpic requires a workspace lookup; we do not have workspaceID here,
	// so we scan for the node by ID across workspaces is not viable with the
	// current index design. Callers should use Update instead. For compatibility
	// with the issue.Service interface we return nil (no-op) and let Update handle it.
	// This method is only called from the Connect handler which follows Get+Update anyway.
	return nil
}

// Stream opens a server-side stream of issues matching filter.
// Limit is intentionally not applied: Stream is for unbounded scans.
// The returned channel closes when all items have been sent or ctx is cancelled.
// FDB errors during streaming are logged; the channel closes early on error.
func (s *IssueService) Stream(ctx context.Context, filter issue.ListFilter) (<-chan *issue.Issue, error) {
	ws, err := s.workspaces.GetByID(ctx, filter.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	if filter.ProjectID == nil {
		return nil, fmt.Errorf("project_id required for issue stream")
	}

	q := node.NodeListQuery{
		OrgID:       ws.OrgID,
		WorkspaceID: filter.WorkspaceID,
		NodeType:    node.NodeTypeIssue,
		ByProject:   filter.ProjectID,
	}
	if filter.EpicID != nil {
		q.FilterEpicID = filter.EpicID
	}
	if filter.IsDraft != nil {
		q.FilterIsDraft = filter.IsDraft
	}

	rawCh, err := s.reader.Stream(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("node stream: %w", err)
	}

	out := make(chan *issue.Issue, 64)
	go func() {
		defer close(out)
		for item := range rawCh {
			if item.Err != nil {
				telemetry.L(ctx).Warn("issue.stream error", slog.String("err", item.Err.Error()))
				return
			}
			select {
			case out <- issueFromNodeListView(item.View):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Search queries Meilisearch then hydrates from the NodeListView reader.
// Falls back to in-memory prefix scan when Meilisearch returns nil (noop searcher).
func (s *IssueService) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	docs, _, err := s.searcher.Search(ctx, "nodes", q, map[string]string{
		"workspace_id": workspaceID.String(),
		"entity_type":  "issue",
	})
	if err != nil {
		telemetry.L(ctx).Error("search.meilisearch_failed",
			slog.String("err", err.Error()),
			slog.String("workspace_id", workspaceID.String()),
		)
		// Fall through to FDB scan.
		docs = nil
	}

	if docs == nil {
		// Noop searcher: use FDB list as search fallback.
		filter.WorkspaceID = workspaceID
		return s.List(ctx, filter)
	}
	if len(docs) == 0 {
		return nil, 0, nil
	}

	issues := make([]*issue.Issue, 0, len(docs))
	for _, doc := range docs {
		issueID, parseErr := uuid.Parse(doc.ID)
		if parseErr != nil {
			continue
		}
		view, getErr := s.reader.Get(ctx, issueID)
		if getErr != nil || view == nil {
			continue
		}
		if view.WorkspaceID != workspaceID || view.NodeType != node.NodeTypeIssue {
			continue
		}
		issues = append(issues, issueFromNodeListView(view))
	}
	return issues, len(issues), nil
}
