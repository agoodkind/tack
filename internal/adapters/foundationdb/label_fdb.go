//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/label"
	"goodkind.io/tack/internal/domain/node"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

type LabelFDBStore struct {
	db       fdb.Database
	entities *EntityStore
	views    *ViewStore
}

func NewLabelFDBStore(db fdb.Database, entities *EntityStore, views *ViewStore) *LabelFDBStore {
	return &LabelFDBStore{
		db:       db,
		entities: entities,
		views:    views,
	}
}

func (s *LabelFDBStore) Create(ctx context.Context, lbl *label.Label) (*label.Label, error) {
	if lbl.ID == uuid.Nil {
		lbl.ID = uuid.New()
	}

	// Resolve org context from workspace
	wsResolve, err := s.views.Resolve(ctx, lbl.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("label create resolve workspace: %w", err)
	}

	orgID := wsResolve.OrgID

	// Determine project ID (nil for workspace-scoped labels)
	var projectID uuid.UUID
	if lbl.ProjectID != nil {
		projectID = *lbl.ProjectID
	}

	// Build NodeValue
	nv := &node.NodeValue{
		ID:          lbl.ID,
		OrgID:       orgID,
		WorkspaceID: lbl.WorkspaceID,
		ProjectID:   projectID,
		NodeType:    node.NodeTypeLabel,
		Name:        lbl.Name,
		CreatedAt:   lbl.CreatedAt,
		UpdatedAt:   lbl.UpdatedAt,
	}

	// Build properties map
	props := make(map[uuid.UUID]*node.PropertyValue)
	props[systemPropID(lbl.WorkspaceID, propNameColor)] = textPV(lbl.Color)
	sortOrderStr := fmt.Sprintf("%v", lbl.SortOrder)
	props[systemPropID(lbl.WorkspaceID, propNameSortOrder)] = textPV(sortOrderStr)

	// Build NodeListView
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          lbl.ID,
		OrgID:       orgID,
		WorkspaceID: lbl.WorkspaceID,
		ProjectID:   projectID,
		NodeType:    node.NodeTypeLabel,
		Name:        lbl.Name,
		SortOrder:   lbl.SortOrder,
		CreatedAt:   lbl.CreatedAt,
		UpdatedAt:   lbl.UpdatedAt,
		CustomProps: make(map[string]json.RawMessage),
	}

	// Add custom props to view
	colorBytes, _ := json.Marshal(lbl.Color)
	view.CustomProps[propNameColor] = colorBytes
	sortOrderBytes, _ := json.Marshal(lbl.SortOrder)
	view.CustomProps[propNameSortOrder] = sortOrderBytes

	// Create using EntityStore.CreateAtomic
	_, err = s.entities.CreateAtomic(ctx, orgID, projectID, nv, props, view, nil, nil, uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("label create atomic: %w", err)
	}

	return lbl, nil
}

func (s *LabelFDBStore) GetByID(ctx context.Context, id uuid.UUID) (*label.Label, error) {
	view, err := s.views.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("label get by id: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return nodeListViewToLabel(view), nil
}

func (s *LabelFDBStore) List(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]*label.Label, error) {
	// Resolve org context
	wsResolve, err := s.views.Resolve(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("label list resolve: %w", err)
	}

	orgID := wsResolve.OrgID

	// If projectID is specified, list labels for that project plus workspace-scoped labels
	if projectID != nil && *projectID != uuid.Nil {
		// List project-scoped labels
		projNVs, err := s.entities.ListByProject(ctx, orgID, *projectID, node.NodeTypeLabel)
		if err != nil {
			return nil, fmt.Errorf("label list by project: %w", err)
		}

		// List workspace-scoped labels
		wsNVs, err := s.entities.ListByProject(ctx, orgID, uuid.Nil, node.NodeTypeLabel)
		if err != nil {
			return nil, fmt.Errorf("label list workspace scope: %w", err)
		}

		// Combine and convert
		var labels []*label.Label
		seen := make(map[uuid.UUID]bool)

		for _, nv := range projNVs {
			lbl, err := s.GetByID(ctx, nv.ID)
			if err != nil && err != domain.ErrNotFound {
				return nil, err
			}
			if lbl != nil {
				labels = append(labels, lbl)
				seen[nv.ID] = true
			}
		}

		for _, nv := range wsNVs {
			if !seen[nv.ID] {
				lbl, err := s.GetByID(ctx, nv.ID)
				if err != nil && err != domain.ErrNotFound {
					return nil, err
				}
				if lbl != nil {
					labels = append(labels, lbl)
				}
			}
		}

		// Sort by sort_order
		sort.Slice(labels, func(i, j int) bool {
			return labels[i].SortOrder < labels[j].SortOrder
		})

		return labels, nil
	}

	// List all workspace-scoped labels
	nvs, err := s.entities.ListByProject(ctx, orgID, uuid.Nil, node.NodeTypeLabel)
	if err != nil {
		return nil, fmt.Errorf("label list workspace: %w", err)
	}

	if len(nvs) == 0 {
		return nil, nil
	}

	// Fetch views and convert
	var labels []*label.Label
	for _, nv := range nvs {
		lbl, err := s.GetByID(ctx, nv.ID)
		if err != nil && err != domain.ErrNotFound {
			return nil, err
		}
		if lbl != nil {
			labels = append(labels, lbl)
		}
	}

	// Sort by sort_order
	sort.Slice(labels, func(i, j int) bool {
		return labels[i].SortOrder < labels[j].SortOrder
	})

	return labels, nil
}

func (s *LabelFDBStore) Update(ctx context.Context, lbl *label.Label) (*label.Label, error) {
	// Fetch existing to get org/workspace context
	existing, err := s.GetByID(ctx, lbl.ID)
	if err != nil {
		return nil, fmt.Errorf("label update fetch: %w", err)
	}

	wsResolve, err := s.views.Resolve(ctx, lbl.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("label update resolve: %w", err)
	}

	orgID := wsResolve.OrgID

	// Determine project ID (nil for workspace-scoped labels)
	var projectID uuid.UUID
	if lbl.ProjectID != nil {
		projectID = *lbl.ProjectID
	}

	// Build updated NodeValue
	nv := &node.NodeValue{
		ID:          lbl.ID,
		OrgID:       orgID,
		WorkspaceID: lbl.WorkspaceID,
		ProjectID:   projectID,
		NodeType:    node.NodeTypeLabel,
		Name:        lbl.Name,
		CreatedAt:   lbl.CreatedAt,
		UpdatedAt:   lbl.UpdatedAt,
	}

	// Build properties
	props := make(map[uuid.UUID]*node.PropertyValue)
	props[systemPropID(lbl.WorkspaceID, propNameColor)] = textPV(lbl.Color)
	sortOrderStr := fmt.Sprintf("%v", lbl.SortOrder)
	props[systemPropID(lbl.WorkspaceID, propNameSortOrder)] = textPV(sortOrderStr)

	// Build updated view
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          lbl.ID,
		OrgID:       orgID,
		WorkspaceID: lbl.WorkspaceID,
		ProjectID:   projectID,
		NodeType:    node.NodeTypeLabel,
		Name:        lbl.Name,
		SortOrder:   lbl.SortOrder,
		CreatedAt:   lbl.CreatedAt,
		UpdatedAt:   lbl.UpdatedAt,
		CustomProps: make(map[string]json.RawMessage),
	}

	// Add custom props to view
	colorBytes, _ := json.Marshal(lbl.Color)
	view.CustomProps[propNameColor] = colorBytes
	sortOrderBytes, _ := json.Marshal(lbl.SortOrder)
	view.CustomProps[propNameSortOrder] = sortOrderBytes

	// Update using entities.Set
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("label update set: %w", err)
	}

	return lbl, nil
}

func (s *LabelFDBStore) Delete(ctx context.Context, id uuid.UUID) error {
	// Get the label to access org/workspace context
	resolve, err := s.views.Resolve(ctx, id)
	if err != nil {
		return fmt.Errorf("label delete resolve: %w", err)
	}

	nv := &node.NodeValue{
		ID:          id,
		OrgID:       resolve.OrgID,
		WorkspaceID: resolve.WorkspaceID,
		ProjectID:   resolve.ProjectID,
		NodeType:    node.NodeTypeLabel,
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("label delete: %w", err)
	}

	return nil
}

func nodeListViewToLabel(v *node.NodeListView) *label.Label {
	if v == nil {
		return nil
	}

	lbl := &label.Label{
		ID:          v.ID,
		WorkspaceID: v.WorkspaceID,
		Name:        v.Name,
		SortOrder:   v.SortOrder,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
		Color:       "",
	}

	// Set project ID if it's not nil
	if v.ProjectID != uuid.Nil {
		lbl.ProjectID = &v.ProjectID
	}

	// Extract custom properties
	if v.CustomProps != nil {
		if colorRaw, ok := v.CustomProps[propNameColor]; ok {
			var s string
			if err := json.Unmarshal(colorRaw, &s); err == nil {
				lbl.Color = s
			}
		}
	}

	return lbl
}

func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	tackPropNamespace := uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

func textPV(s string) *node.PropertyValue {
	return &node.PropertyValue{Kind: node.PropertyValueText, Text: &s}
}

const (
	propNameColor     = "color"
	propNameSortOrder = "sort_order"
)
