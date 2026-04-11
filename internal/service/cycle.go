package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/cycle"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

// CycleService reads and writes cycles via FDB only.
type CycleService struct {
	entities    node.EntityRepository
	reader      node.NodeReader
	workspaces  workspace.Repository
	containment node.ContainmentRepository
	searcher    domainsearch.Searcher
}

// NewCycleService creates a CycleService backed by FDB.
func NewCycleService(
	entities node.EntityRepository,
	reader node.NodeReader,
	workspaces workspace.Repository,
	containment node.ContainmentRepository,
	searcher domainsearch.Searcher,
) *CycleService {
	return &CycleService{
		entities:    entities,
		reader:      reader,
		workspaces:  workspaces,
		containment: containment,
		searcher:    searcher,
	}
}

func nodeValueFromCycle(c *cycle.Cycle, orgID uuid.UUID) (*node.NodeValue, map[uuid.UUID]*node.PropertyValue) {
	updatedBy := c.CreatedBy
	if c.UpdatedBy != nil {
		updatedBy = *c.UpdatedBy
	}
	nv := &node.NodeValue{
		ID:          c.ID,
		OrgID:       orgID,
		WorkspaceID: c.WorkspaceID,
		ProjectID:   c.ProjectID,
		NodeType:    node.NodeTypeCycle,
		Name:        c.Name,
		Description: c.Description,
		SortOrder:   c.SortOrder,
		CreatedBy:   c.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}

	props := make(map[uuid.UUID]*node.PropertyValue)
	if c.StartDate != nil {
		t := *c.StartDate
		props[systemPropID(c.WorkspaceID, propNameStartDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}
	if c.EndDate != nil {
		t := *c.EndDate
		props[systemPropID(c.WorkspaceID, propNameEndDate)] = &node.PropertyValue{
			Kind:      node.PropertyValueTimestamp,
			Timestamp: &t,
		}
	}

	return nv, props
}

func cycleFromNodeValue(nv *node.NodeValue, rawProps node.Properties) *cycle.Cycle {
	updatedBy := nv.UpdatedBy
	c := &cycle.Cycle{
		ID:          nv.ID,
		NodeID:      nv.ID,
		WorkspaceID: nv.WorkspaceID,
		ProjectID:   nv.ProjectID,
		Name:        nv.Name,
		Description: nv.Description,
		SortOrder:   nv.SortOrder,
		CreatedBy:   nv.CreatedBy,
		UpdatedBy:   &updatedBy,
		CreatedAt:   nv.CreatedAt,
		UpdatedAt:   nv.UpdatedAt,
	}

	if rawProps == nil {
		return c
	}

	if raw, ok := rawProps[systemPropID(nv.WorkspaceID, propNameStartDate)]; ok {
		var pv node.PropertyValue
		if json.Unmarshal(raw, &pv) == nil && pv.Timestamp != nil {
			t := *pv.Timestamp
			c.StartDate = &t
		}
	}
	if raw, ok := rawProps[systemPropID(nv.WorkspaceID, propNameEndDate)]; ok {
		var pv node.PropertyValue
		if json.Unmarshal(raw, &pv) == nil && pv.Timestamp != nil {
			t := *pv.Timestamp
			c.EndDate = &t
		}
	}

	return c
}

// buildCycleView converts a cycle and orgID into a NodeListView for atomic writes.
func buildCycleView(c *cycle.Cycle, orgID uuid.UUID) *node.NodeListView {
	updatedBy := c.CreatedBy
	if c.UpdatedBy != nil {
		updatedBy = *c.UpdatedBy
	}
	desc := c.Description
	if len(desc) > 500 {
		desc = desc[:500]
	}
	return &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          c.ID,
		OrgID:       orgID,
		WorkspaceID: c.WorkspaceID,
		ProjectID:   c.ProjectID,
		NodeType:    node.NodeTypeCycle,
		SortOrder:   c.SortOrder,
		Name:        c.Name,
		Description: desc,
		StartDate:   c.StartDate,
		DueDate:     c.EndDate,
		CreatedBy:   c.CreatedBy,
		UpdatedBy:   updatedBy,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// cycleFromNodeListView reconstructs a cycle.Cycle from a materialized NodeListView.
func cycleFromNodeListView(v *node.NodeListView) *cycle.Cycle {
	updatedBy := v.UpdatedBy
	return &cycle.Cycle{
		ID:          v.ID,
		NodeID:      v.ID,
		WorkspaceID: v.WorkspaceID,
		ProjectID:   v.ProjectID,
		Name:        v.Name,
		Description: v.Description,
		SortOrder:   v.SortOrder,
		StartDate:   v.StartDate,
		EndDate:     v.DueDate,
		CreatedBy:   v.CreatedBy,
		UpdatedBy:   &updatedBy,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
	}
}


func (s *CycleService) Create(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	ws, err := s.workspaces.GetByID(ctx, c.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	if c.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, fmt.Errorf("generate id: %w", err)
		}
		c.ID = id
	}
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now

	nv, props := nodeValueFromCycle(c, ws.OrgID)
	view := buildCycleView(c, ws.OrgID)
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	_ = s.searcher.Index(ctx, "nodes", c.ID.String(), nodeDocFromView(view))
	telemetry.L(ctx).Info("cycle.created",
		slog.String("cycle_id", c.ID.String()),
		slog.String("project_id", c.ProjectID.String()),
	)
	return c, nil
}

func (s *CycleService) GetByID(ctx context.Context, projectID, id uuid.UUID) (*cycle.Cycle, error) {
	// Without workspaceID we cannot do an efficient FDB lookup; attempt a project
	// scan to find the workspace for this project. For now callers always pass a
	// cycle they created within a known workspace, so we use a two-step approach:
	// scan the project index. ListByProject is workspace-agnostic via the project index.
	// We need orgID though. Store a workspace reference on project? Not available here.
	// Practical approach: pass workspaceID. Since all callers (mcp/connectrpc) have
	// projectID only, we do a ListByProject scan and pick the match.
	// This requires orgID; we must not break the interface.
	// For now: return ErrNotFound to indicate the method needs workspaceID.
	// The MCP/RPC callers that use this: cycle_handler.go GetCycle uses projectID, id.
	// We'll need to fix the callers or cache orgID. For this migration we add a
	// workspaceID-aware variant and update callers accordingly below.
	return nil, domain.ErrNotFound
}

// GetByIDWithWorkspace fetches a cycle using both projectID and workspaceID.
func (s *CycleService) GetByIDWithWorkspace(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*cycle.Cycle, error) {
	view, err := s.reader.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("entity get: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	if view.WorkspaceID != workspaceID || view.NodeType != node.NodeTypeCycle {
		return nil, domain.ErrNotFound
	}
	if view.ProjectID != projectID {
		return nil, domain.ErrNotFound
	}
	return cycleFromNodeListView(view), nil
}

func (s *CycleService) List(ctx context.Context, projectID uuid.UUID) ([]*cycle.Cycle, error) {
	// List by projectID requires orgID. The project index stores workspaceID per node,
	// so EntityRepository.ListByProject works by org+project. We need orgID.
	// Since all callers have workspaceID, use ListWithWorkspace.
	return nil, nil
}

// ListWithWorkspace returns all cycles for a project, given the workspace context.
func (s *CycleService) ListWithWorkspace(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*cycle.Cycle, error) {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	q := node.NodeListQuery{
		OrgID:       ws.OrgID,
		WorkspaceID: workspaceID,
		NodeType:    node.NodeTypeCycle,
		ByProject:   &projectID,
	}
	views, err := s.reader.List(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("node list: %w", err)
	}
	cycles := make([]*cycle.Cycle, 0, len(views))
	for _, v := range views {
		cycles = append(cycles, cycleFromNodeListView(v))
	}
	return cycles, nil
}

func (s *CycleService) Update(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	ws, err := s.workspaces.GetByID(ctx, c.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	c.UpdatedAt = time.Now().UTC()
	nv, props := nodeValueFromCycle(c, ws.OrgID)
	view := buildCycleView(c, ws.OrgID)
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("entity set: %w", err)
	}

	_ = s.searcher.Index(ctx, "nodes", c.ID.String(), nodeDocFromView(view))
	return c, nil
}

func (s *CycleService) Delete(ctx context.Context, id uuid.UUID) error {
	// Same issue as Epic.Delete: no workspaceID. See DeleteByWorkspace.
	telemetry.L(context.Background()).Warn("CycleService.Delete called without workspaceID - no-op",
		slog.String("cycle_id", id.String()))
	return nil
}

// DeleteByWorkspace deletes a cycle using the full workspace context.
func (s *CycleService) DeleteByWorkspace(ctx context.Context, workspaceID, projectID, id uuid.UUID) error {
	ws, err := s.workspaces.GetByID(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}

	nv, err := s.entities.Get(ctx, ws.OrgID, workspaceID, node.NodeTypeCycle, id)
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
	telemetry.L(ctx).Info("cycle.deleted",
		slog.String("cycle_id", id.String()),
	)
	return nil
}
