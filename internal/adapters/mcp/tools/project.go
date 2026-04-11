package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ProjectCreator is the minimal interface needed for project creation with default states.
type ProjectCreator interface {
	Create(ctx context.Context, p *project.Project) (*project.Project, error)
}

func RegisterProject(s *mcpserver.MCPServer, projects project.Repository, svc ProjectCreator, states state.Repository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{Name: "tack_list_projects", Description: "Lists all projects in a workspace identified by workspace_slug. Returns each project's name and identifier (e.g. ENG). Use the identifier as project_identifier in issue/epic/cycle/module tools.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}}, Required: []string{"workspace_slug"}}}, listProjects(projects, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_get_project", Description: "Fetches a project by workspace_slug and project_identifier (e.g. ENG), including its workflow states. Use tack_list_projects to find identifiers.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "project_identifier": map[string]any{"type": "string", "description": "The project identifier"}}, Required: []string{"workspace_slug", "project_identifier"}}}, getProject(projects, states, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_create_project", Description: "Creates a project in the given workspace and seeds it with default workflow states. identifier must be uppercase letters e.g. ENG. Returns the created project.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "name": map[string]any{"type": "string", "description": "The project name"}, "identifier": map[string]any{"type": "string", "description": "The project identifier"}, "description": map[string]any{"type": "string", "description": "The project description"}}, Required: []string{"workspace_slug", "name", "identifier"}}}, createProject(svc, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_update_project", Description: "Updates project fields by workspace_slug and project_identifier. Only provided fields change; omitted fields are unchanged. Returns the updated project.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "project_identifier": map[string]any{"type": "string", "description": "The project identifier"}, "name": map[string]any{"type": "string", "description": "The project name"}, "identifier": map[string]any{"type": "string", "description": "The project identifier"}, "description": map[string]any{"type": "string", "description": "The project description"}, "default_state_id": map[string]any{"type": "string", "description": "The default state ID"}}, Required: []string{"workspace_slug", "project_identifier"}}}, updateProject(projects, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_delete_project", Description: "Permanently deletes a project and all its data by workspace_slug and project_identifier. Deletion is irreversible.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "project_identifier": map[string]any{"type": "string", "description": "The project identifier"}}, Required: []string{"workspace_slug", "project_identifier"}}}, deleteProject(projects, r))
}

type ListProjectsInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
}

func listProjects(projects project.Repository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListProjectsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		ps, err := projects.List(ctx, ws.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"projects": ps}, ""), nil
	}
}

type GetProjectInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func getProject(projects project.Repository, states state.Repository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetProjectInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		_, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		ss, _ := states.List(ctx, proj.ID)
		return Success(map[string]any{"project": proj, "states": ss}, ""), nil
	}
}

type CreateProjectInput struct {
	WorkspaceSlug string  `json:"workspace_slug"`
	Name          string  `json:"name"`
	Identifier    string  `json:"identifier"`
	Description   *string `json:"description,omitempty"`
}

func createProject(svc ProjectCreator, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateProjectInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"project": p}, "Project created. Call tack_list_states to see the seeded workflow states."), nil
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

func updateProject(projects project.Repository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in UpdateProjectInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
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
				return RecoverableError(parseErr.Error()), nil
			}
			proj.DefaultStateID = &id
		}
		proj.UpdatedBy = &userID
		updated, err := projects.Update(ctx, proj)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"project": updated}, ""), nil
	}
}

type DeleteProjectInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

type DeleteProjectOutput struct {
	OK bool `json:"ok"`
}

func deleteProject(projects project.Repository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteProjectInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		_, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if err := projects.Delete(ctx, proj.ID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteProjectOutput{OK: true}, "Deletion is permanent and irreversible."), nil
	}
}
