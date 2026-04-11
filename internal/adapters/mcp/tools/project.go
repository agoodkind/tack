package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProjectCreator is the minimal interface needed for project creation with default states.
type ProjectCreator interface {
	Create(ctx context.Context, p *project.Project) (*project.Project, error)
}

func RegisterProject(s *mcp.Server, projects project.Repository, svc ProjectCreator, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_projects", Description: "Lists all projects in a workspace. Returns project ID, name, identifier (e.g. ENG), and description. Use identifiers here as project_identifier in issue/epic/cycle/module tools."}, listProjects(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_project", Description: "Fetches a project by workspace ID and project ID, including its workflow states. Use tack_list_projects to find project IDs."}, getProject(projects, states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_project", Description: "Creates a project and seeds it with default workflow states. identifier must be uppercase letters e.g. ENG. Returns the created project."}, createProject(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_project", Description: "Updates project fields. Only provided fields change; omitted fields are unchanged. Returns the updated project."}, updateProject(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_project", Description: "Permanently deletes a project and all its data. Deletion is irreversible."}, deleteProject(projects))
}

type ListProjectsInput struct {
	WorkspaceID string `json:"workspace_id"`
}

func listProjects(projects project.Repository) mcp.ToolHandlerFor[ListProjectsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		ps, err := projects.List(ctx, wsID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"projects": ps}, ""), nil, nil
	}
}

type GetProjectInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

func getProject(projects project.Repository, states state.Repository) mcp.ToolHandlerFor[GetProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetProjectInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		ss, _ := states.List(ctx, p.ID)
		return Success(map[string]any{"project": p, "states": ss}, ""), nil, nil
	}
}

type CreateProjectInput struct {
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Identifier  string  `json:"identifier"`
	Description *string `json:"description,omitempty"`
}

func createProject(svc ProjectCreator) mcp.ToolHandlerFor[CreateProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateProjectInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		newProject := &project.Project{
			WorkspaceID: wsID,
			Name:        in.Name,
			Identifier:  in.Identifier,
			CreatedBy:   userID,
		}
		if in.Description != nil {
			newProject.Description = *in.Description
		}
		p, err := svc.Create(ctx, newProject)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"project": p}, "Project created. Call tack_list_states to see the seeded workflow states."), nil, nil
	}
}

type UpdateProjectInput struct {
	WorkspaceID    string  `json:"workspace_id"`
	ProjectID      string  `json:"project_id"`
	Name           *string `json:"name,omitempty"`
	Identifier     *string `json:"identifier,omitempty"`
	Description    *string `json:"description,omitempty"`
	DefaultStateID *string `json:"default_state_id,omitempty"`
}

func updateProject(projects project.Repository) mcp.ToolHandlerFor[UpdateProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		if in.Name != nil {
			p.Name = *in.Name
		}
		if in.Identifier != nil {
			p.Identifier = *in.Identifier
		}
		if in.Description != nil {
			p.Description = *in.Description
		}
		if in.DefaultStateID != nil {
			id, parseErr := parseUUID(*in.DefaultStateID, "default_state_id")
			if parseErr != nil {
				return RecoverableError(parseErr.Error()), nil, nil
			}
			p.DefaultStateID = &id
		}
		p.UpdatedBy = &userID
		updated, err := projects.Update(ctx, p)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"project": updated}, ""), nil, nil
	}
}

type DeleteProjectInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

type DeleteProjectOutput struct {
	OK bool `json:"ok"`
}

func deleteProject(projects project.Repository) mcp.ToolHandlerFor[DeleteProjectInput, DeleteProjectOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteProjectInput) (*mcp.CallToolResult, DeleteProjectOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), DeleteProjectOutput{}, nil
		}
		if err := projects.Delete(ctx, pID); err != nil {
			return ClassifyError(ctx, err), DeleteProjectOutput{}, nil
		}
		return Success(DeleteProjectOutput{OK: true}, "Deletion is permanent and irreversible."), DeleteProjectOutput{}, nil
	}
}
