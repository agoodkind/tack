package service

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/state"
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

type ProjectService struct {
	projects project.Repository
	states   state.Repository
}

func NewProjectService(projects project.Repository, states state.Repository) *ProjectService {
	return &ProjectService{projects: projects, states: states}
}

// Create creates a project and seeds it with the default state set.
func (s *ProjectService) Create(ctx context.Context, p *project.Project) (*project.Project, error) {
	created, err := s.projects.Create(ctx, p)
	if err != nil {
		return nil, err
	}

	for _, ds := range defaultStates {
		st, err := s.states.Create(ctx, &state.State{
			ProjectID: created.ID,
			Name:      ds.Name,
			GroupName: ds.Group,
			Color:     ds.Color,
			SortOrder: ds.SortOrder,
		})
		if err != nil {
			return created, err
		}
		// Set the first state (Backlog) as the project default.
		if ds.Group == state.GroupBacklog && created.DefaultStateID == nil {
			created.DefaultStateID = &st.ID
			_, _ = s.projects.Update(ctx, created)
		}
	}

	return created, nil
}
