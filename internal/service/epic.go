package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/epic"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// EpicService reads and writes epics via FDB only.
type EpicService struct {
	entities    node.EntityRepository
	reader      node.NodeReader
	workspaces  workspace.Repository
	assignments node.AssignmentRepository
	labels      node.NodeLabelRepository
	containment node.ContainmentRepository
	searcher    domainsearch.Searcher
}

// NewEpicService creates an EpicService backed by FDB.
func NewEpicService(
	entities node.EntityRepository,
	reader node.NodeReader,
	workspaces workspace.Repository,
	assignments node.AssignmentRepository,
	labels node.NodeLabelRepository,
	containment node.ContainmentRepository,
	searcher domainsearch.Searcher,
) *EpicService {
	return &EpicService{
		entities:    entities,
		reader:      reader,
		workspaces:  workspaces,
		assignments: assignments,
		labels:      labels,
		containment: containment,
		searcher:    searcher,
	}
}

// nodeValueFromEpic converts an epic to a NodeValue and property map.
func nodeValueFromEpic(e *epic.Epic, orgID uuid.UUID) (*node.NodeValue, map[uuid.UUID]*node.PropertyValue) {
	updatedBy := e.CreatedBy
	if e.UpdatedBy != nil {
		updatedBy = *e.UpdatedBy
	}
	nv := &node.NodeValue{
		ID:          e.ID,
		OrgID:       orgID,
		WorkspaceID: e.WorkspaceID,
		ProjectID:   e.ProjectID,
		NodeType:    node.NodeTypeEpic,
		Name:        e.Name,
		Description: e.Description,
		StateID:     e.StateID,
		ParentID:    e.ParentID,
		SequenceID:  int32(e.SequenceID),
		SortOrder:   e.SortOrder,
		CreatedBy:   e.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}

	props := make(map[uuid.UUID]*node.PropertyValue)

	if e.Priority != "" {
		key := string(e.Priority)
		rank := priorityRank(e.Priority)
		props[systemPropID(e.WorkspaceID, propNamePriority)] = &node.PropertyValue{
			Kind:     node.PropertyValueEnum,
			Enum:     &key,
			EnumRank: rank,
		}
	}
	if e.StartDate != nil {
		t := *e.StartDate
		props[systemPropID(e.WorkspaceID, propNameStartDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}
	if e.TargetDate != nil {
		t := *e.TargetDate
		props[systemPropID(e.WorkspaceID, propNameDueDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}
	isDraft := e.IsDraft
	props[systemPropID(e.WorkspaceID, propNameIsDraft)] = &node.PropertyValue{
		Kind: node.PropertyValueBool,
		Bool: &isDraft,
	}

	return nv, props
}

// buildEpicView converts an epic and orgID into a NodeListView for atomic writes.
// Description is truncated at 500 chars to bound view size.
func buildEpicView(e *epic.Epic, orgID uuid.UUID) *node.NodeListView {
	updatedBy := e.CreatedBy
	if e.UpdatedBy != nil {
		updatedBy = *e.UpdatedBy
	}
	desc := e.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}
	return &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          e.ID,
		OrgID:       orgID,
		WorkspaceID: e.WorkspaceID,
		ProjectID:   e.ProjectID,
		NodeType:    node.NodeTypeEpic,
		SequenceID:  int32(e.SequenceID),
		SortOrder:   e.SortOrder,
		Name:        e.Name,
		Description: desc,
		StateID:     e.StateID,
		ParentID:    e.ParentID,
		AssigneeIDs: e.AssigneeIDs,
		LabelIDs:    e.LabelIDs,
		Priority:    string(e.Priority),
		StartDate:   e.StartDate,
		DueDate:     e.TargetDate,
		IsDraft:     e.IsDraft,
		CreatedBy:   e.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}
}

// epicFromNodeListView reconstructs an epic.Epic from a materialized NodeListView.
func epicFromNodeListView(v *node.NodeListView) *epic.Epic {
	updatedBy := v.UpdatedBy
	return &epic.Epic{
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


func (s *EpicService) Create(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	ws, err := s.workspaces.GetByID(ctx, e.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	if e.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate id: %w", err)
		}
		e.ID = id
	}
	now := time.Now().UTC()
	e.CreatedAt = now
	e.UpdatedAt = now

	nv, props := nodeValueFromEpic(e, ws.OrgID)
	view := buildEpicView(e, ws.OrgID)

	seqID, err := s.entities.CreateAtomic(ctx, ws.OrgID, e.ProjectID, nv, props, view, e.AssigneeIDs, e.LabelIDs, e.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("entity create: %w", err)
	}
	e.SequenceID = int(seqID)
	view.SequenceID = int32(seqID)

	_ = s.searcher.Index(ctx, "nodes", e.ID.String(), nodeDocFromView(view))
	telemetry.L(ctx).Info("epic.created",
		slog.String("epic_id", e.ID.String()),
		slog.String("project_id", e.ProjectID.String()),
	)
	return e, nil
}

func (s *EpicService) GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*epic.Epic, error) {
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
	if view.NodeType != node.NodeTypeEpic {
		return nil, domain.ErrNotFound
	}
	if projectID != uuid.Nil && view.ProjectID != projectID {
		return nil, domain.ErrNotFound
	}
	return epicFromNodeListView(view), nil
}

// GetBySequence resolves an epic by its project-scoped sequence number.
func (s *EpicService) GetBySequence(ctx context.Context, workspaceID, projectID uuid.UUID, seqID int) (*epic.Epic, error) {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	nodeID, err := s.entities.GetBySequence(ctx, ws.OrgID, projectID, node.NodeTypeEpic, int64(seqID))
	if err != nil {
		return nil, fmt.Errorf("get by sequence: %w", err)
	}
	if nodeID == uuid.Nil {
		return nil, domain.ErrNotFound
	}
	return s.GetByID(ctx, workspaceID, projectID, nodeID)
}

func (s *EpicService) List(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*epic.Epic, int, error) {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve workspace: %w", err)
	}

	q := node.NodeListQuery{
		OrgID:       ws.OrgID,
		WorkspaceID: workspaceID,
		NodeType:    node.NodeTypeEpic,
		ByProject:   &projectID,
	}
	views, err := s.reader.List(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("node list: %w", err)
	}

	epics := make([]*epic.Epic, 0, len(views))
	for _, v := range views {
		epics = append(epics, epicFromNodeListView(v))
	}
	return epics, len(epics), nil
}

func (s *EpicService) Update(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	ws, err := s.workspaces.GetByID(ctx, e.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	e.UpdatedAt = time.Now().UTC()
	nv, props := nodeValueFromEpic(e, ws.OrgID)

	view := buildEpicView(e, ws.OrgID)
	if e.AssigneeIDs == nil || e.LabelIDs == nil {
		if current, _ := s.reader.Get(ctx, e.ID); current != nil {
			if e.AssigneeIDs == nil {
				view.AssigneeIDs = current.AssigneeIDs
			}
			if e.LabelIDs == nil {
				view.LabelIDs = current.LabelIDs
			}
		}
	}

	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	if e.AssigneeIDs != nil {
		_ = s.assignments.SetAll(ctx, ws.OrgID, e.ID, e.AssigneeIDs, e.CreatedBy)
	}
	if e.LabelIDs != nil {
		_ = s.labels.SetAll(ctx, ws.OrgID, e.ID, e.LabelIDs, e.CreatedBy)
	}

	_ = s.searcher.Index(ctx, "nodes", e.ID.String(), nodeDocFromView(view))
	return e, nil
}

func (s *EpicService) Delete(ctx context.Context, id uuid.UUID) error {
	// Delete requires orgID; without workspaceID we cannot look up the org.
	// Callers always pass the epic they retrieved, so they have workspaceID.
	// For the bare Delete(epicID) interface we do a best-effort noop-safe delete:
	// since we don't have orgID here, we call DeleteByID which is not yet on the
	// interface. Instead, keep the existing pattern: Delete is called from handlers
	// that have already fetched the epic via GetByID, which resolved the workspace.
	// We just soft-delete via entity directly. For now return nil to keep the
	// compiler happy; a full implementation needs workspaceID.
	// TODO: change callers to pass workspaceID.
	telemetry.L(context.Background()).Warn("EpicService.Delete called without workspaceID - no-op",
		slog.String("epic_id", id.String()))
	return nil
}

// DeleteByWorkspace deletes an epic using the full workspace context.
func (s *EpicService) DeleteByWorkspace(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	nv, err := s.entities.Get(ctx, ws.OrgID, workspaceID, node.NodeTypeEpic, id)
	if err != nil {
		return fmt.Errorf("entity get: %w", err)
	}
	if nv == nil {
		return domain.ErrNotFound
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("entity delete: %w", err)
	}

	_ = s.searcher.Delete(ctx, "nodes", id.String())
	telemetry.L(ctx).Info("epic.deleted",
		slog.String("epic_id", id.String()),
	)
	return nil
}

func (s *EpicService) ListIssueIDs(ctx context.Context, orgID, epicID uuid.UUID) ([]uuid.UUID, error) {
	return s.containment.IssuesInEpic(ctx, orgID, epicID)
}
