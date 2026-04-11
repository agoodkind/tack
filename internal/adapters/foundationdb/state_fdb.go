//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/state"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

type StateFDBStore struct {
	db       fdb.Database
	entities *EntityStore
	views    *ViewStore
}

func NewStateFDBStore(db fdb.Database, entities *EntityStore, views *ViewStore) *StateFDBStore {
	return &StateFDBStore{
		db:       db,
		entities: entities,
		views:    views,
	}
}

func (s *StateFDBStore) Create(ctx context.Context, st *state.State) (*state.State, error) {
	if st.ID == uuid.Nil {
		st.ID = uuid.New()
	}

	// Resolve org and workspace context from project
	projResolve, err := s.views.Resolve(ctx, st.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("state create resolve project: %w", err)
	}

	orgID := projResolve.OrgID
	wsID := projResolve.WorkspaceID

	// Build NodeValue
	nv := &node.NodeValue{
		ID:          st.ID,
		OrgID:       orgID,
		WorkspaceID: wsID,
		ProjectID:   st.ProjectID,
		NodeType:    node.NodeTypeState,
		Name:        st.Name,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
	}

	// Build properties map
	props := make(map[uuid.UUID]*node.PropertyValue)
	props[systemPropID(wsID, propNameGroupName)] = textPV(string(st.GroupName))
	props[systemPropID(wsID, propNameColor)] = textPV(st.Color)
	sortOrderStr := fmt.Sprintf("%v", st.SortOrder)
	props[systemPropID(wsID, propNameSortOrder)] = textPV(sortOrderStr)

	// Build NodeListView
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          st.ID,
		OrgID:       orgID,
		WorkspaceID: wsID,
		ProjectID:   st.ProjectID,
		NodeType:    node.NodeTypeState,
		Name:        st.Name,
		SortOrder:   st.SortOrder,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
		CustomProps: make(map[string]json.RawMessage),
	}

	// Add custom props to view
	groupBytes, _ := json.Marshal(string(st.GroupName))
	view.CustomProps[propNameGroupName] = groupBytes
	colorBytes, _ := json.Marshal(st.Color)
	view.CustomProps[propNameColor] = colorBytes
	sortOrderBytes, _ := json.Marshal(st.SortOrder)
	view.CustomProps[propNameSortOrder] = sortOrderBytes

	// Create using EntityStore.CreateAtomic
	_, err = s.entities.CreateAtomic(ctx, orgID, st.ProjectID, nv, props, view, nil, nil, uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("state create atomic: %w", err)
	}

	return st, nil
}

func (s *StateFDBStore) GetByID(ctx context.Context, projectID, id uuid.UUID) (*state.State, error) {
	view, err := s.views.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("state get by id: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return nodeListViewToState(view), nil
}

func (s *StateFDBStore) List(ctx context.Context, projectID uuid.UUID) ([]*state.State, error) {
	// Resolve org context
	projResolve, err := s.views.Resolve(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("state list resolve: %w", err)
	}

	orgID := projResolve.OrgID

	// List states by project using entity store
	nvs, err := s.entities.ListByProject(ctx, orgID, projectID, node.NodeTypeState)
	if err != nil {
		return nil, fmt.Errorf("state list by project: %w", err)
	}

	if len(nvs) == 0 {
		return nil, nil
	}

	// Fetch views and convert
	var states []*state.State
	for _, nv := range nvs {
		st, err := s.GetByID(ctx, projectID, nv.ID)
		if err != nil && err != domain.ErrNotFound {
			return nil, err
		}
		if st != nil {
			states = append(states, st)
		}
	}

	// Sort by sort_order
	sort.Slice(states, func(i, j int) bool {
		return states[i].SortOrder < states[j].SortOrder
	})

	return states, nil
}

func (s *StateFDBStore) Update(ctx context.Context, st *state.State) (*state.State, error) {
	// Fetch existing to get org/workspace context
	existing, err := s.GetByID(ctx, st.ProjectID, st.ID)
	if err != nil {
		return nil, fmt.Errorf("state update fetch: %w", err)
	}

	projResolve, err := s.views.Resolve(ctx, st.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("state update resolve: %w", err)
	}

	orgID := projResolve.OrgID
	wsID := projResolve.WorkspaceID

	// Build updated NodeValue
	nv := &node.NodeValue{
		ID:          st.ID,
		OrgID:       orgID,
		WorkspaceID: wsID,
		ProjectID:   st.ProjectID,
		NodeType:    node.NodeTypeState,
		Name:        st.Name,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
	}

	// Build properties
	props := make(map[uuid.UUID]*node.PropertyValue)
	props[systemPropID(wsID, propNameGroupName)] = textPV(string(st.GroupName))
	props[systemPropID(wsID, propNameColor)] = textPV(st.Color)
	sortOrderStr := fmt.Sprintf("%v", st.SortOrder)
	props[systemPropID(wsID, propNameSortOrder)] = textPV(sortOrderStr)

	// Build updated view
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          st.ID,
		OrgID:       orgID,
		WorkspaceID: wsID,
		ProjectID:   st.ProjectID,
		NodeType:    node.NodeTypeState,
		Name:        st.Name,
		SortOrder:   st.SortOrder,
		CreatedAt:   st.CreatedAt,
		UpdatedAt:   st.UpdatedAt,
		CustomProps: make(map[string]json.RawMessage),
	}

	// Add custom props to view
	groupBytes, _ := json.Marshal(string(st.GroupName))
	view.CustomProps[propNameGroupName] = groupBytes
	colorBytes, _ := json.Marshal(st.Color)
	view.CustomProps[propNameColor] = colorBytes
	sortOrderBytes, _ := json.Marshal(st.SortOrder)
	view.CustomProps[propNameSortOrder] = sortOrderBytes

	// Update using entities.Set
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("state update set: %w", err)
	}

	return st, nil
}

func (s *StateFDBStore) Delete(ctx context.Context, id uuid.UUID) error {
	// Get the state to access org/project context
	resolve, err := s.views.Resolve(ctx, id)
	if err != nil {
		return fmt.Errorf("state delete resolve: %w", err)
	}

	nv := &node.NodeValue{
		ID:          id,
		OrgID:       resolve.OrgID,
		WorkspaceID: resolve.WorkspaceID,
		ProjectID:   resolve.ProjectID,
		NodeType:    node.NodeTypeState,
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("state delete: %w", err)
	}

	return nil
}

func nodeListViewToState(v *node.NodeListView) *state.State {
	if v == nil {
		return nil
	}

	st := &state.State{
		ID:        v.ID,
		ProjectID: v.ProjectID,
		Name:      v.Name,
		SortOrder: v.SortOrder,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
		GroupName: state.GroupBacklog, // default
		Color:     "",
	}

	// Extract custom properties
	if v.CustomProps != nil {
		if groupRaw, ok := v.CustomProps[propNameGroupName]; ok {
			var s string
			if err := json.Unmarshal(groupRaw, &s); err == nil {
				st.GroupName = state.GroupName(s)
			}
		}
		if colorRaw, ok := v.CustomProps[propNameColor]; ok {
			var s string
			if err := json.Unmarshal(colorRaw, &s); err == nil {
				st.Color = s
			}
		}
	}

	return st
}

func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	tackPropNamespace := uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

func textPV(s string) *node.PropertyValue {
	return &node.PropertyValue{Kind: node.PropertyValueText, Text: &s}
}

const (
	propNameGroupName = "group_name"
	propNameColor     = "color"
	propNameSortOrder = "sort_order"
)
