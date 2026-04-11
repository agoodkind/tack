package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/module"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// registerModuleTools registers all module-related MCP tools using slug-derived names.
func registerModuleTools(s *mcpserver.MCPServer, slug, pluralSlug string, modules *service.ModuleService, containment node.ContainmentRepository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_" + pluralSlug,
		Description: "Lists " + pluralSlug + " (feature groupings) in a project. Returns name, status, and description for each.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier"},
		},
	}, listModules(modules, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_get_" + slug,
		Description: "Fetches a single " + slug + " by module_id UUID. Use tack_list_" + pluralSlug + " first to find the ID.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"module_id":          map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "module_id"},
		},
	}, getModule(modules, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_create_" + slug,
		Description: "Creates a new " + slug + " in a project. status defaults to 'backlog' if omitted.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
				"description":        map[string]any{"type": "string"},
				"status":             map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "name"},
		},
	}, createModule(modules, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_update_" + slug,
		Description: "Updates fields on a " + slug + " by module_id UUID. Only provided fields change.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"module_id":          map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
				"description":        map[string]any{"type": "string"},
				"status":             map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "module_id"},
		},
	}, updateModule(modules, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_delete_" + slug,
		Description: "Deletes a " + slug + " by module_id UUID. Deletion is permanent and irreversible.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"module_id":          map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "module_id"},
		},
	}, deleteModule(modules, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_add_to_" + slug,
		Description: "Adds issues to a " + slug + " by their UUID issue IDs.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
				"module_id":      map[string]any{"type": "string"},
				"issue_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			Required: []string{"workspace_slug", "module_id", "issue_ids"},
		},
	}, addToModule(containment, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_remove_from_" + slug,
		Description: "Removes an issue from a " + slug + ".",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
				"module_id":      map[string]any{"type": "string"},
				"issue_id":       map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "module_id", "issue_id"},
		},
	}, removeFromModule(containment, r))
}

type ListModulesInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listModules(modules *service.ModuleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListModulesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		ms, err := modules.ListWithWorkspace(ctx, ws.ID, proj.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"modules": ms}, ""), nil
	}
}

type GetModuleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	ModuleID          string `json:"module_id"`
}

func getModule(modules *service.ModuleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetModuleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		m, err := modules.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, moduleID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"module": m}, ""), nil
	}
}

type CreateModuleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Status            *string `json:"status,omitempty"`
}

func createModule(modules *service.ModuleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateModuleInput
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"module": m}, "Use tack_add_to_module to add issues to this module."), nil
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

func updateModule(modules *service.ModuleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in UpdateModuleInput
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
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		existing, err := modules.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, moduleID)
		if err != nil {
			return ClassifyError(ctx, err), nil
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"module": updated}, ""), nil
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

func deleteModule(modules *service.ModuleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteModuleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := modules.DeleteByWorkspace(ctx, ws.ID, proj.ID, moduleID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteModuleOutput{OK: true}, "Deletion is permanent and irreversible."), nil
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

func addToModule(containment node.ContainmentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in AddToModuleInput
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
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		var added int
		for _, s := range in.IssueIDs {
			issueID, err := parseUUID(s, "issue_id")
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			if err := containment.AddIssueToModule(ctx, ws.OrgID, moduleID, issueID, userID); err != nil {
				return ClassifyError(ctx, err), nil
			}
			added++
		}
		return Success(AddToModuleOutput{Added: added}, ""), nil
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

func removeFromModule(containment node.ContainmentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in RemoveFromModuleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		moduleID, err := parseUUID(in.ModuleID, "module_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		issueID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := containment.RemoveIssueFromModule(ctx, ws.OrgID, moduleID, issueID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(RemoveFromModuleOutput{OK: true}, ""), nil
	}
}
