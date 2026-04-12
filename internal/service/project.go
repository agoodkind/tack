package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
)

// defaultStates are seeded into every new project.
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

// ProjectService creates projects and seeds default states.
type ProjectService struct {
	projects project.Repository
	entities node.EntityRepository
	reader   node.NodeReader
	searcher domainsearch.Searcher
}

// NewProjectService creates a ProjectService with the given dependencies.
func NewProjectService(projects project.Repository, entities node.EntityRepository, reader node.NodeReader, searcher domainsearch.Searcher) *ProjectService {
	return &ProjectService{projects: projects, entities: entities, reader: reader, searcher: searcher}
}

// Create creates a project, seeds default states, and indexes it for search.
func (s *ProjectService) Create(ctx context.Context, p *project.Project) (*project.Project, error) {
	created, err := s.projects.Create(ctx, p)
	if err != nil {
		return nil, err
	}

	// Resolve org/workspace context for the newly created project.
	resolve, err := s.reader.Resolve(ctx, created.ID)
	if err != nil {
		return created, err
	}
	orgID := resolve.OrgID
	wsID := resolve.WorkspaceID

	for _, ds := range defaultStates {
		stID := uuid.New()
		now := time.Now()

		groupBytes, _ := json.Marshal(string(ds.Group))
		colorBytes, _ := json.Marshal(ds.Color)
		sortBytes, _ := json.Marshal(ds.SortOrder)

		nv := &node.NodeValue{
			ID:          stID,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   created.ID,
			NodeType:    node.NodeTypeState,
			Name:        ds.Name,
			SortOrder:   ds.SortOrder,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		props := map[uuid.UUID]*node.PropertyValue{
			systemPropID(wsID, propNameGroupName): textPV(string(ds.Group)),
			systemPropID(wsID, propNameColor):     textPV(ds.Color),
			systemPropID(wsID, propNameSortOrder): textPV(fmt.Sprintf("%v", ds.SortOrder)),
		}
		view := &node.NodeListView{
			Version:     node.ViewVersion1,
			ID:          stID,
			OrgID:       orgID,
			WorkspaceID: wsID,
			ProjectID:   created.ID,
			NodeType:    node.NodeTypeState,
			Name:        ds.Name,
			SortOrder:   ds.SortOrder,
			CreatedAt:   now,
			UpdatedAt:   now,
			CustomProps: map[string]json.RawMessage{
				propNameGroupName: groupBytes,
				propNameColor:     colorBytes,
				propNameSortOrder: sortBytes,
			},
		}

		if _, err := s.entities.CreateAtomic(ctx, orgID, created.ID, nv, props, view, nil, nil, uuid.Nil); err != nil {
			return created, err
		}

		// Set the first state (Backlog) as the project default.
		if ds.Group == state.GroupBacklog && created.DefaultStateID == nil {
			created.DefaultStateID = &stID
			_, _ = s.projects.Update(ctx, created)
		}
	}

	_ = s.searcher.Index(ctx, "nodes", created.ID.String(), domainsearch.NodeDoc{
		ID:          created.ID.String(),
		WorkspaceID: created.WorkspaceID.String(),
		ProjectID:   created.ID.String(),
		EntityType:  "project",
		Name:        created.Name,
		Description: created.Description,
	})

	return created, nil
}
