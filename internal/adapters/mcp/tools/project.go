package tools

import (
	"context"
	"fmt"

	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProjectCreator is the minimal interface needed for project creation with default states.
type ProjectCreator interface {
	Create(ctx context.Context, p *project.Project) (*project.Project, error)
}

func RegisterProject(s *mcp.Server, projects project.Repository, svc ProjectCreator, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_projects", Description: "List all projects in a workspace"}, listProjects(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_project", Description: "Get a project by ID"}, getProject(projects, states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_project", Description: "Create a new project and seed it with default workflow states"}, createProject(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_project", Description: "Update project fields (partial — only provided fields are changed)"}, updateProject(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_project", Description: "Permanently delete a project and all its data"}, deleteProject(projects))
}

type ListProjectsInput struct {
	WorkspaceID string `json:"workspace_id"`
}

func listProjects(projects project.Repository) mcp.ToolHandlerFor[ListProjectsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		ps, err := projects.List(ctx, wsID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"projects": ps}, nil
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
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return nil, nil, err
		}
		ss, _ := states.List(ctx, p.ID)
		return nil, map[string]any{"project": p, "states": ss}, nil
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
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
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
			return nil, nil, fmt.Errorf("create project: %w", err)
		}
		return nil, map[string]any{"project": p}, nil
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
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return nil, nil, err
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
				return nil, nil, parseErr
			}
			p.DefaultStateID = &id
		}
		p.UpdatedBy = &userID
		updated, err := projects.Update(ctx, p)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"project": updated}, nil
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
			return nil, DeleteProjectOutput{}, err
		}
		if err := projects.Delete(ctx, pID); err != nil {
			return nil, DeleteProjectOutput{}, err
		}
		return nil, DeleteProjectOutput{OK: true}, nil
	}
}
