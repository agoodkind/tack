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

func RegisterProject(s *mcp.Server, projects project.Repository, svc ProjectCreator, states state.Repository, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_projects", Description: "Lists all projects in a workspace identified by workspace_slug. Returns each project's name and identifier (e.g. ENG). Use the identifier as project_identifier in issue/epic/cycle/module tools."}, listProjects(projects, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_project", Description: "Fetches a project by workspace_slug and project_identifier (e.g. ENG), including its workflow states. Use tack_list_projects to find identifiers."}, getProject(projects, states, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_project", Description: "Creates a project in the given workspace and seeds it with default workflow states. identifier must be uppercase letters e.g. ENG. Returns the created project."}, createProject(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_project", Description: "Updates project fields by workspace_slug and project_identifier. Only provided fields change; omitted fields are unchanged. Returns the updated project."}, updateProject(projects, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_project", Description: "Permanently deletes a project and all its data by workspace_slug and project_identifier. Deletion is irreversible."}, deleteProject(projects, r))
}

type ListProjectsInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
}

func listProjects(projects project.Repository, r *Resolver) mcp.ToolHandlerFor[ListProjectsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, any, error) {
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		ps, err := projects.List(ctx, ws.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"projects": ps}, ""), nil, nil
	}
}

type GetProjectInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func getProject(projects project.Repository, states state.Repository, r *Resolver) mcp.ToolHandlerFor[GetProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetProjectInput) (*mcp.CallToolResult, any, error) {
		_, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		ss, _ := states.List(ctx, proj.ID)
		return Success(map[string]any{"project": proj, "states": ss}, ""), nil, nil
	}
}

type CreateProjectInput struct {
	WorkspaceSlug string  `json:"workspace_slug"`
	Name          string  `json:"name"`
	Identifier    string  `json:"identifier"`
	Description   *string `json:"description,omitempty"`
}

func createProject(svc ProjectCreator, r *Resolver) mcp.ToolHandlerFor[CreateProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateProjectInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		newProject := &project.Project{
			WorkspaceID: ws.ID,
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
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              *string `json:"name,omitempty"`
	Identifier        *string `json:"identifier,omitempty"`
	Description       *string `json:"description,omitempty"`
	DefaultStateID    *string `json:"default_state_id,omitempty"`
}

func updateProject(projects project.Repository, r *Resolver) mcp.ToolHandlerFor[UpdateProjectInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		_ = ws
		if in.Name != nil {
			proj.Name = *in.Name
		}
		if in.Identifier != nil {
			proj.Identifier = *in.Identifier
		}
		if in.Description != nil {
			proj.Description = *in.Description
		}
		if in.DefaultStateID != nil {
			id, parseErr := parseUUID(*in.DefaultStateID, "default_state_id")
			if parseErr != nil {
				return RecoverableError(parseErr.Error()), nil, nil
			}
			proj.DefaultStateID = &id
		}
		proj.UpdatedBy = &userID
		updated, err := projects.Update(ctx, proj)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"project": updated}, ""), nil, nil
	}
}

type DeleteProjectInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

type DeleteProjectOutput struct {
	OK bool `json:"ok"`
}

func deleteProject(projects project.Repository, r *Resolver) mcp.ToolHandlerFor[DeleteProjectInput, DeleteProjectOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteProjectInput) (*mcp.CallToolResult, DeleteProjectOutput, error) {
		_, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), DeleteProjectOutput{}, nil
		}
		if err := projects.Delete(ctx, proj.ID); err != nil {
			return ClassifyError(ctx, err), DeleteProjectOutput{}, nil
		}
		return Success(DeleteProjectOutput{OK: true}, "Deletion is permanent and irreversible."), DeleteProjectOutput{}, nil
	}
}
