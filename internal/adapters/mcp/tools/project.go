package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterProject(s *mcp.Server, projects project.Repository, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_projects", Description: "List all projects in a workspace"}, listProjects(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_project", Description: "Get a project by ID"}, getProject(projects, states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_project", Description: "Create a new project in a workspace"}, createProject(projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_project", Description: "Update project fields (partial — only provided fields are changed)"}, updateProject(projects))
}

type ListProjectsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
}
type ListProjectsOutput struct {
	Projects json.RawMessage `json:"projects"`
}

func listProjects(projects project.Repository) mcp.ToolHandlerFor[ListProjectsInput, ListProjectsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, ListProjectsOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, ListProjectsOutput{}, err
		}
		ps, err := projects.List(ctx, wsID)
		if err != nil {
			return nil, ListProjectsOutput{}, err
		}
		b, _ := json.Marshal(ps)
		return nil, ListProjectsOutput{Projects: b}, nil
	}
}

type GetProjectInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
}
type GetProjectOutput struct {
	Project json.RawMessage `json:"project"`
	States  json.RawMessage `json:"states"`
}

func getProject(projects project.Repository, states state.Repository) mcp.ToolHandlerFor[GetProjectInput, GetProjectOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetProjectInput) (*mcp.CallToolResult, GetProjectOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, GetProjectOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, GetProjectOutput{}, err
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return nil, GetProjectOutput{}, err
		}
		ss, _ := states.List(ctx, p.ID)
		pb, _ := json.Marshal(p)
		sb, _ := json.Marshal(ss)
		return nil, GetProjectOutput{Project: pb, States: sb}, nil
	}
}

type CreateProjectInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	Name        string `json:"name"         jsonschema:"required"`
	Identifier  string `json:"identifier"   jsonschema:"required"`
	Description string `json:"description" `
}
type CreateProjectOutput struct {
	Project json.RawMessage `json:"project"`
}

func createProject(projects project.Repository) mcp.ToolHandlerFor[CreateProjectInput, CreateProjectOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateProjectInput) (*mcp.CallToolResult, CreateProjectOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, CreateProjectOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateProjectOutput{}, err
		}
		p, err := projects.Create(ctx, &project.Project{
			WorkspaceID: wsID,
			Name:        in.Name,
			Identifier:  in.Identifier,
			Description: in.Description,
			CreatedBy:   userID,
		})
		if err != nil {
			return nil, CreateProjectOutput{}, fmt.Errorf("create project: %w", err)
		}
		b, _ := json.Marshal(p)
		return nil, CreateProjectOutput{Project: b}, nil
	}
}

type UpdateProjectInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	Name        string `json:"name"        `
	Description string `json:"description" `
}
type UpdateProjectOutput struct {
	Project json.RawMessage `json:"project"`
}

func updateProject(projects project.Repository) mcp.ToolHandlerFor[UpdateProjectInput, UpdateProjectOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectInput) (*mcp.CallToolResult, UpdateProjectOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, UpdateProjectOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, UpdateProjectOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateProjectOutput{}, err
		}
		p, err := projects.GetByID(ctx, wsID, pID)
		if err != nil {
			return nil, UpdateProjectOutput{}, err
		}
		if in.Name != "" {
			p.Name = in.Name
		}
		if in.Description != "" {
			p.Description = in.Description
		}
		p.UpdatedBy = &userID
		updated, err := projects.Update(ctx, p)
		if err != nil {
			return nil, UpdateProjectOutput{}, err
		}
		b, _ := json.Marshal(updated)
		return nil, UpdateProjectOutput{Project: b}, nil
	}
}
