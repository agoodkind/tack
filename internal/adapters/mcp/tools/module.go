package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/module"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterModule(s *mcp.Server, modules module.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_modules", Description: "List modules (feature groupings) in a project"}, listModules(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_module", Description: "Get a module by ID"}, getModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_module", Description: "Create a new module"}, createModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_module", Description: "Update module fields (partial)"}, updateModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_module", Description: "Delete a module"}, deleteModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_module", Description: "Add issues to a module"}, addToModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_module", Description: "Remove an issue from a module"}, removeFromModule(modules))
}

type ListModulesInput struct {
	ProjectID string `json:"project_id"`
}
type ListModulesOutput struct {
	Modules any `json:"modules"`
}

func listModules(modules module.Repository) mcp.ToolHandlerFor[ListModulesInput, ListModulesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListModulesInput) (*mcp.CallToolResult, ListModulesOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, ListModulesOutput{}, err
		}
		ms, err := modules.List(ctx, pID)
		if err != nil {
			return nil, ListModulesOutput{}, err
		}
		return nil, ListModulesOutput{Modules: ms}, nil
	}
}

type GetModuleInput struct {
	ProjectID string `json:"project_id"`
	ModuleID  string `json:"module_id"` 
}
type GetModuleOutput struct {
	Module any `json:"module"`
}

func getModule(modules module.Repository) mcp.ToolHandlerFor[GetModuleInput, GetModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetModuleInput) (*mcp.CallToolResult, GetModuleOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, GetModuleOutput{}, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, GetModuleOutput{}, err
		}
		m, err := modules.GetByID(ctx, pID, mID)
		if err != nil {
			return nil, GetModuleOutput{}, err
		}
		return nil, GetModuleOutput{Module: m}, nil
	}
}

type CreateModuleInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}
type CreateModuleOutput struct {
	Module any `json:"module"`
}

func createModule(modules module.Repository) mcp.ToolHandlerFor[CreateModuleInput, CreateModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateModuleInput) (*mcp.CallToolResult, CreateModuleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, CreateModuleOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateModuleOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, CreateModuleOutput{}, err
		}
		status := "backlog"
		if in.Status != nil && *in.Status != "" {
			status = *in.Status
		}
		newModule := &module.Module{
			WorkspaceID: wsID,
			ProjectID:   pID,
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
			return nil, CreateModuleOutput{}, err
		}
		return nil, CreateModuleOutput{Module: m}, nil
	}
}

type UpdateModuleInput struct {
	ProjectID   string  `json:"project_id"`
	ModuleID    string  `json:"module_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}
type UpdateModuleOutput struct {
	Module any `json:"module"`
}

func updateModule(modules module.Repository) mcp.ToolHandlerFor[UpdateModuleInput, UpdateModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateModuleInput) (*mcp.CallToolResult, UpdateModuleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, UpdateModuleOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateModuleOutput{}, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, UpdateModuleOutput{}, err
		}
		existing, err := modules.GetByID(ctx, pID, mID)
		if err != nil {
			return nil, UpdateModuleOutput{}, err
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
			return nil, UpdateModuleOutput{}, err
		}
		return nil, UpdateModuleOutput{Module: updated}, nil
	}
}

type DeleteModuleInput struct {
	ModuleID string `json:"module_id"`
}
type DeleteModuleOutput struct {
	OK bool `json:"ok"`
}

func deleteModule(modules module.Repository) mcp.ToolHandlerFor[DeleteModuleInput, DeleteModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteModuleInput) (*mcp.CallToolResult, DeleteModuleOutput, error) {
		id, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, DeleteModuleOutput{}, err
		}
		if err := modules.Delete(ctx, id); err != nil {
			return nil, DeleteModuleOutput{}, err
		}
		return nil, DeleteModuleOutput{OK: true}, nil
	}
}

type AddToModuleInput struct {
	ModuleID string   `json:"module_id,omitempty"`
	IssueIDs []string `json:"issue_ids,omitempty"`
}
type AddToModuleOutput struct {
	Added int `json:"added"`
}

func addToModule(modules module.Repository) mcp.ToolHandlerFor[AddToModuleInput, AddToModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToModuleInput) (*mcp.CallToolResult, AddToModuleOutput, error) {
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		ids := make([]uuid.UUID, 0, len(in.IssueIDs))
		for _, s := range in.IssueIDs {
			id, err := parseUUID(s, "issue_id")
			if err != nil {
				return nil, AddToModuleOutput{}, err
			}
			ids = append(ids, id)
		}
		if err := modules.AddIssues(ctx, mID, ids); err != nil {
			return nil, AddToModuleOutput{}, err
		}
		return nil, AddToModuleOutput{Added: len(ids)}, nil
	}
}

type RemoveFromModuleInput struct {
	ModuleID string `json:"module_id"`
	IssueID  string `json:"issue_id"` 
}
type RemoveFromModuleOutput struct {
	OK bool `json:"ok"`
}

func removeFromModule(modules module.Repository) mcp.ToolHandlerFor[RemoveFromModuleInput, RemoveFromModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromModuleInput) (*mcp.CallToolResult, RemoveFromModuleOutput, error) {
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		if err := modules.RemoveIssue(ctx, mID, iID); err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		return nil, RemoveFromModuleOutput{OK: true}, nil
	}
}
