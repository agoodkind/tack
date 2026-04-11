package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/module"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// ModuleService reads and writes modules via FDB only.
type ModuleService struct {
	entities    node.EntityRepository
	reader      node.NodeReader
	workspaces  workspace.Repository
	containment node.ContainmentRepository
	searcher    domainsearch.Searcher
}

// NewModuleService creates a ModuleService backed by FDB.
func NewModuleService(
	entities node.EntityRepository,
	reader node.NodeReader,
	workspaces workspace.Repository,
	containment node.ContainmentRepository,
	searcher domainsearch.Searcher,
) *ModuleService {
	return &ModuleService{
		entities:    entities,
		reader:      reader,
		workspaces:  workspaces,
		containment: containment,
		searcher:    searcher,
	}
}

func nodeValueFromModule(m *module.Module, orgID uuid.UUID) (*node.NodeValue, map[uuid.UUID]*node.PropertyValue) {
	updatedBy := m.CreatedBy
	if m.UpdatedBy != nil {
		updatedBy = *m.UpdatedBy
	}
	nv := &node.NodeValue{
		ID:          m.ID,
		OrgID:       orgID,
		WorkspaceID: m.WorkspaceID,
		ProjectID:   m.ProjectID,
		NodeType:    node.NodeTypeModule,
		Name:        m.Name,
		Description: m.Description,
		SortOrder:   m.SortOrder,
		CreatedBy:   m.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}

	props := make(map[uuid.UUID]*node.PropertyValue)

	// Status stored as text property
	if m.Status != "" {
		props[systemPropID(m.WorkspaceID, propNameStatus)] = textPV(m.Status)
	}
	if m.StartDate != nil {
		t := *m.StartDate
		props[systemPropID(m.WorkspaceID, propNameStartDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}
	if m.TargetDate != nil {
		t := *m.TargetDate
		props[systemPropID(m.WorkspaceID, propNameDueDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}

	return nv, props
}

func moduleFromNodeValue(nv *node.NodeValue, rawProps node.Properties) *module.Module {
	updatedBy := nv.UpdatedBy
	m := &module.Module{
		ID:          nv.ID,
		NodeID:      nv.ID,
		WorkspaceID: nv.WorkspaceID,
		ProjectID:   nv.ProjectID,
		Name:        nv.Name,
		Description: nv.Description,
		Status:      "backlog",
		SortOrder:   nv.SortOrder,
		CreatedBy:   nv.CreatedBy,
		UpdatedBy:   &updatedBy,
		CreatedAt:   nv.CreatedAt,
		UpdatedAt:   nv.UpdatedAt,
	}

	if rawProps == nil {
		return m
	}

	if raw, ok := rawProps[systemPropID(nv.WorkspaceID, propNameStatus)]; ok {
		var pv node.PropertyValue
		if json.Unmarshal(raw, &pv) == nil && pv.Text != nil {
			m.Status = *pv.Text
		}
	}
	if raw, ok := rawProps[systemPropID(nv.WorkspaceID, propNameStartDate)]; ok {
		var pv node.PropertyValue
		if json.Unmarshal(raw, &pv) == nil && pv.Timestamp != nil {
			t := *pv.Timestamp
			m.StartDate = &t
		}
	}
	if raw, ok := rawProps[systemPropID(nv.WorkspaceID, propNameDueDate)]; ok {
		var pv node.PropertyValue
		if json.Unmarshal(raw, &pv) == nil && pv.Timestamp != nil {
			t := *pv.Timestamp
			m.TargetDate = &t
		}
	}

	return m
}

// buildModuleView converts a module and orgID into a NodeListView for atomic writes.
func buildModuleView(m *module.Module, orgID uuid.UUID) *node.NodeListView {
	updatedBy := m.CreatedBy
	if m.UpdatedBy != nil {
		updatedBy = *m.UpdatedBy
	}
	desc := m.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}
	return &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          m.ID,
		OrgID:       orgID,
		WorkspaceID: m.WorkspaceID,
		ProjectID:   m.ProjectID,
		NodeType:    node.NodeTypeModule,
		SortOrder:   m.SortOrder,
		Name:        m.Name,
		Description: desc,
		Status:      m.Status,
		StartDate:   m.StartDate,
		DueDate:     m.TargetDate,
		CreatedBy:   m.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// moduleFromNodeListView reconstructs a module.Module from a materialized NodeListView.
func moduleFromNodeListView(v *node.NodeListView) *module.Module {
	updatedBy := v.UpdatedBy
	status := v.Status
	if status == "" {
		status = "backlog"
	}
	return &module.Module{
		ID:          v.ID,
		NodeID:      v.ID,
		WorkspaceID: v.WorkspaceID,
		ProjectID:   v.ProjectID,
		Name:        v.Name,
		Description: v.Description,
		Status:      status,
		SortOrder:   v.SortOrder,
		StartDate:   v.StartDate,
		TargetDate:  v.DueDate,
		CreatedBy:   v.CreatedBy,
		UpdatedBy:   &updatedBy,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
	}
}


func (s *ModuleService) Create(ctx context.Context, m *module.Module) (*module.Module, error) {
	ws, err := s.workspaces.GetByID(ctx, m.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	if m.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate id: %w", err)
		}
		m.ID = id
	}
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	nv, props := nodeValueFromModule(m, ws.OrgID)
	view := buildModuleView(m, ws.OrgID)
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	_ = s.searcher.Index(ctx, "nodes", m.ID.String(), nodeDocFromView(view))
	telemetry.L(ctx).Info("module.created",
		slog.String("module_id", m.ID.String()),
		slog.String("project_id", m.ProjectID.String()),
	)
	return m, nil
}

func (s *ModuleService) GetByID(ctx context.Context, projectID, id uuid.UUID) (*module.Module, error) {
	// projectID-only lookup is not viable without workspaceID. Return ErrNotFound;
	// callers should use GetByIDWithWorkspace.
	return nil, domain.ErrNotFound
}

// GetByIDWithWorkspace fetches a module with full workspace context.
func (s *ModuleService) GetByIDWithWorkspace(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*module.Module, error) {
	view, err := s.reader.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("entity get: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	if view.WorkspaceID != workspaceID || view.NodeType != node.NodeTypeModule {
		return nil, domain.ErrNotFound
	}
	if view.ProjectID != projectID {
		return nil, domain.ErrNotFound
	}
	return moduleFromNodeListView(view), nil
}

func (s *ModuleService) List(ctx context.Context, projectID uuid.UUID) ([]*module.Module, error) {
	// Without workspaceID cannot look up orgID. Return empty; use ListWithWorkspace.
	return nil, nil
}

// ListWithWorkspace returns all modules for a project.
func (s *ModuleService) ListWithWorkspace(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*module.Module, error) {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	q := node.NodeListQuery{
		OrgID:       ws.OrgID,
		WorkspaceID: workspaceID,
		NodeType:    node.NodeTypeModule,
		ByProject:   &projectID,
	}
	views, err := s.reader.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("node list: %w", err)
	}
	modules := make([]*module.Module, 0, len(views))
	for _, v := range views {
		modules = append(modules, moduleFromNodeListView(v))
	}
	return modules, nil
}

func (s *ModuleService) Update(ctx context.Context, m *module.Module) (*module.Module, error) {
	ws, err := s.workspaces.GetByID(ctx, m.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	m.UpdatedAt = time.Now().UTC()
	nv, props := nodeValueFromModule(m, ws.OrgID)
	view := buildModuleView(m, ws.OrgID)
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	_ = s.searcher.Index(ctx, "nodes", m.ID.String(), nodeDocFromView(view))
	return m, nil
}

func (s *ModuleService) Delete(ctx context.Context, id uuid.UUID) error {
	telemetry.L(context.Background()).Warn("ModuleService.Delete called without workspaceID - no-op",
		slog.String("module_id", id.String()))
	return nil
}

// DeleteByWorkspace deletes a module using the full workspace context.
func (s *ModuleService) DeleteByWorkspace(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	nv, err := s.entities.Get(ctx, ws.OrgID, workspaceID, node.NodeTypeModule, id)
	if err != nil {
		return fmt.Errorf("entity get: %w", err)
	}
	if nv == nil {
		return domain.ErrNotFound
	}
	if nv.ProjectID != projectID {
		return domain.ErrNotFound
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("entity delete: %w", err)
	}

	_ = s.searcher.Delete(ctx, "nodes", id.String())
	telemetry.L(ctx).Info("module.deleted",
		slog.String("module_id", id.String()),
	)
	return nil
}
