package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/module"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerModuleTools registers all module-related MCP tools using slug-derived names.
func registerModuleTools(s *mcp.Server, slug, pluralSlug string, modules *service.ModuleService, containment node.ContainmentRepository, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_" + pluralSlug, Description: "List " + pluralSlug + " (feature groupings) in a project"}, listModules(modules, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_" + slug, Description: "Get a " + slug + " by ID"}, getModule(modules, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_" + slug, Description: "Create a new " + slug}, createModule(modules, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_" + slug, Description: "Update " + slug + " fields (partial)"}, updateModule(modules, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_" + slug, Description: "Delete a " + slug}, deleteModule(modules, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_" + slug, Description: "Add issues to a " + slug}, addToModule(containment, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_" + slug, Description: "Remove an issue from a " + slug}, removeFromModule(containment, r))
}

type ListModulesInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listModules(modules *service.ModuleService, r *Resolver) mcp.ToolHandlerFor[ListModulesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListModulesInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		ms, err := modules.ListWithWorkspace(ctx, ws.ID, proj.ID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"modules": ms}, nil
	}
}

type GetModuleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	ModuleID          string `json:"module_id"`
}

func getModule(modules *service.ModuleService, r *Resolver) mcp.ToolHandlerFor[GetModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetModuleInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, nil, err
		}
		m, err := modules.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, moduleID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"module": m}, nil
	}
}

type CreateModuleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Status            *string `json:"status,omitempty"`
}

func createModule(modules *service.ModuleService, r *Resolver) mcp.ToolHandlerFor[CreateModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateModuleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		status := "backlog"
		if in.Status != nil && *in.Status != "" {
			status = *in.Status
		}
		newModule := &module.Module{
			WorkspaceID: ws.ID,
			ProjectID:   proj.ID,
			Name:        in.Name,
			Status:      status,
			SortOrder:   65535,
			CreatedBy:   userID,
		}
		if in.Description != nil {
			newModule.Description = *in.Description
		}
		m, err := modules.Create(ctx, newModule)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"module": m}, nil
	}
}

type UpdateModuleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	ModuleID          string  `json:"module_id"`
	Name              *string `json:"name,omitempty"`
	Description       *string `json:"description,omitempty"`
	Status            *string `json:"status,omitempty"`
}

func updateModule(modules *service.ModuleService, r *Resolver) mcp.ToolHandlerFor[UpdateModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateModuleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := modules.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, moduleID)
		if err != nil {
			return nil, nil, err
		}
		if in.Name != nil {
			existing.Name = *in.Name
		}
		if in.Description != nil {
			existing.Description = *in.Description
		}
		if in.Status != nil {
			existing.Status = *in.Status
		}
		existing.UpdatedBy = &userID
		updated, err := modules.Update(ctx, existing)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"module": updated}, nil
	}
}

type DeleteModuleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	ModuleID          string `json:"module_id"`
}
type DeleteModuleOutput struct {
	OK bool `json:"ok"`
}

func deleteModule(modules *service.ModuleService, r *Resolver) mcp.ToolHandlerFor[DeleteModuleInput, DeleteModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteModuleInput) (*mcp.CallToolResult, DeleteModuleOutput, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, DeleteModuleOutput{}, err
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, DeleteModuleOutput{}, err
		}
		if err := modules.DeleteByWorkspace(ctx, ws.ID, proj.ID, moduleID); err != nil {
			return nil, DeleteModuleOutput{}, err
		}
		return nil, DeleteModuleOutput{OK: true}, nil
	}
}

type AddToModuleInput struct {
	WorkspaceSlug string   `json:"workspace_slug"`
	ModuleID      string   `json:"module_id"`
	IssueIDs      []string `json:"issue_ids"`
}
type AddToModuleOutput struct {
	Added int `json:"added"`
}

func addToModule(containment node.ContainmentRepository, r *Resolver) mcp.ToolHandlerFor[AddToModuleInput, AddToModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToModuleInput) (*mcp.CallToolResult, AddToModuleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		var added int
		for _, s := range in.IssueIDs {
			issueID, err := parseUUID(s, "issue_id")
			if err != nil {
				return nil, AddToModuleOutput{}, err
			}
			if err := containment.AddIssueToModule(ctx, ws.OrgID, moduleID, issueID, userID); err != nil {
				return nil, AddToModuleOutput{}, err
			}
			added++
		}
		return nil, AddToModuleOutput{Added: added}, nil
	}
}

type RemoveFromModuleInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	ModuleID      string `json:"module_id"`
	IssueID       string `json:"issue_id"`
}
type RemoveFromModuleOutput struct {
	OK bool `json:"ok"`
}

func removeFromModule(containment node.ContainmentRepository, r *Resolver) mcp.ToolHandlerFor[RemoveFromModuleInput, RemoveFromModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromModuleInput) (*mcp.CallToolResult, RemoveFromModuleOutput, error) {
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		issueID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		if err := containment.RemoveIssueFromModule(ctx, ws.OrgID, moduleID, issueID); err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		return nil, RemoveFromModuleOutput{OK: true}, nil
	}
}
