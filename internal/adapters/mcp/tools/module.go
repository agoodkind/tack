package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/module"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterModule(s *mcp.Server, modules module.Repository, containment node.ContainmentRepository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_modules", Description: "List modules (feature groupings) in a project"}, listModules(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_module", Description: "Get a module by ID"}, getModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_module", Description: "Create a new module"}, createModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_module", Description: "Update module fields (partial)"}, updateModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_module", Description: "Delete a module"}, deleteModule(modules))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_module", Description: "Add issues to a module"}, addToModule(containment))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_module", Description: "Remove an issue from a module"}, removeFromModule(containment))
}

type ListModulesInput struct {
	ProjectID string `json:"project_id"`
}

func listModules(modules module.Repository) mcp.ToolHandlerFor[ListModulesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListModulesInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		ms, err := modules.List(ctx, pID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"modules": ms}, nil
	}
}

type GetModuleInput struct {
	ProjectID string `json:"project_id"`
	ModuleID  string `json:"module_id"`
}

func getModule(modules module.Repository) mcp.ToolHandlerFor[GetModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetModuleInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, nil, err
		}
		m, err := modules.GetByID(ctx, pID, mID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"module": m}, nil
	}
}

type CreateModuleInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func createModule(modules module.Repository) mcp.ToolHandlerFor[CreateModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateModuleInput) (*mcp.CallToolResult, any, error) {
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
			return nil, nil, err
		}
		return nil, map[string]any{"module": m}, nil
	}
}

type UpdateModuleInput struct {
	ProjectID   string  `json:"project_id"`
	ModuleID    string  `json:"module_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

func updateModule(modules module.Repository) mcp.ToolHandlerFor[UpdateModuleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateModuleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := modules.GetByID(ctx, pID, mID)
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
	OrgID    string   `json:"org_id"`
	ModuleID string   `json:"module_id"`
	IssueIDs []string `json:"issue_ids"`
}
type AddToModuleOutput struct {
	Added int `json:"added"`
}

func addToModule(containment node.ContainmentRepository) mcp.ToolHandlerFor[AddToModuleInput, AddToModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToModuleInput) (*mcp.CallToolResult, AddToModuleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, AddToModuleOutput{}, err
		}
		var added int
		for _, s := range in.IssueIDs {
			iID, err := parseUUID(s, "issue_id")
			if err != nil {
				return nil, AddToModuleOutput{}, err
			}
			if err := containment.AddIssueToModule(ctx, orgID, mID, iID, userID); err != nil {
				return nil, AddToModuleOutput{}, err
			}
			added++
		}
		return nil, AddToModuleOutput{Added: added}, nil
	}
}

type RemoveFromModuleInput struct {
	OrgID    string `json:"org_id"`
	ModuleID string `json:"module_id"`
	IssueID  string `json:"issue_id"`
}
type RemoveFromModuleOutput struct {
	OK bool `json:"ok"`
}

func removeFromModule(containment node.ContainmentRepository) mcp.ToolHandlerFor[RemoveFromModuleInput, RemoveFromModuleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromModuleInput) (*mcp.CallToolResult, RemoveFromModuleOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		mID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		if err := containment.RemoveIssueFromModule(ctx, orgID, mID, iID); err != nil {
			return nil, RemoveFromModuleOutput{}, err
		}
		return nil, RemoveFromModuleOutput{OK: true}, nil
	}
}
