package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/state"
)

var defaultStates = []struct {
	Name      string
	Group     state.GroupName
	Color     string
	SortOrder float64
}{
	{"Backlog", state.GroupBacklog, "#9B9B9B", 1},
	{"Todo", state.GroupTodo, "#26B5CE", 2},
	{"In Progress", state.GroupStarted, "#F96E19", 3},
	{"Done", state.GroupCompleted, "#16A34A", 4},
	{"Canceled", state.GroupCancelled, "#EF4444", 5},
}

type ProjectService struct {
	entities node.EntityRepository
	reader   node.NodeReader
	searcher domainsearch.Searcher
}

func NewProjectService(entities node.EntityRepository, reader node.NodeReader, searcher domainsearch.Searcher) *ProjectService {
	return &ProjectService{entities: entities, reader: reader, searcher: searcher}
}

// Create creates a project node, seeds default states, and indexes for search.
func (s *ProjectService) Create(ctx context.Context, wsID uuid.UUID, name, identifier, description string, createdBy uuid.UUID) (*node.NodeListView, error) {
	wsResolve, err := s.reader.Resolve(ctx, wsID)
	if err != nil {
		return nil, err
	}
	orgID := wsResolve.OrgID

	now := time.Now()
	projID := uuid.New()

	identBytes, _ := json.Marshal(identifier)
	customProps := map[string]json.RawMessage{"identifier": identBytes}
	if description != "" {
		descBytes, _ := json.Marshal(description)
		customProps["description"] = descBytes
	}

	nv := &node.NodeValue{
		ID: projID, OrgID: orgID, WorkspaceID: wsID, ProjectID: projID,
		NodeType: node.NodeTypeProject, Name: name, Description: description,
		CreatedBy: createdBy, UpdatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeListView{
		Version: node.ViewVersion1, ID: projID, OrgID: orgID, WorkspaceID: wsID,
		ProjectID: projID, NodeType: node.NodeTypeProject, Name: name,
		CreatedBy: createdBy, UpdatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
		CustomProps: customProps,
	}

	// Write identifier property for indexed lookup
	identPropID := node.SystemPropID(wsID, "identifier")
	props := map[uuid.UUID]*node.PropertyValue{
		identPropID: node.TextPropertyValue(identifier),
	}

	if _, err := s.entities.CreateAtomic(ctx, orgID, projID, nv, props, view, nil, nil, createdBy); err != nil {
		return nil, err
	}

	// Seed default states
	var defaultStateID uuid.UUID
	for i, ds := range defaultStates {
		stID := uuid.New()
		groupBytes, _ := json.Marshal(string(ds.Group))
		colorBytes, _ := json.Marshal(ds.Color)
		sortBytes, _ := json.Marshal(ds.SortOrder)

		stNV := &node.NodeValue{
			ID: stID, OrgID: orgID, WorkspaceID: wsID, ProjectID: projID,
			NodeType: node.NodeTypeState, Name: ds.Name, SortOrder: ds.SortOrder,
			CreatedAt: now, UpdatedAt: now,
		}
		stProps := map[uuid.UUID]*node.PropertyValue{
			node.SystemPropID(wsID, propNameGroupName): node.TextPropertyValue(string(ds.Group)),
			node.SystemPropID(wsID, propNameColor):     node.TextPropertyValue(ds.Color),
			node.SystemPropID(wsID, propNameSortOrder): node.TextPropertyValue(fmt.Sprintf("%v", ds.SortOrder)),
		}
		stView := &node.NodeListView{
			Version: node.ViewVersion1, ID: stID, OrgID: orgID, WorkspaceID: wsID,
			ProjectID: projID, NodeType: node.NodeTypeState, Name: ds.Name,
			SortOrder: ds.SortOrder, CreatedAt: now, UpdatedAt: now,
			CustomProps: map[string]json.RawMessage{
				propNameGroupName: groupBytes, propNameColor: colorBytes, propNameSortOrder: sortBytes,
			},
		}

		if _, err := s.entities.CreateAtomic(ctx, orgID, projID, stNV, stProps, stView, nil, nil, uuid.Nil); err != nil {
			return nil, err
		}
		if i == 0 {
			defaultStateID = stID
		}
	}

	// Update project with default state
	stateBytes, _ := json.Marshal(defaultStateID.String())
	view.CustomProps["default_state_id"] = stateBytes
	nv.UpdatedAt = now
	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, err
	}

	_ = s.searcher.Index(ctx, "nodes", projID.String(), domainsearch.NodeDoc{
		ID: projID.String(), WorkspaceID: wsID.String(), ProjectID: projID.String(),
		EntityType: "project", Name: name, Description: description,
	})

	return view, nil
}
