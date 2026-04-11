package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/cycle"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// registerCycleTools registers all cycle-related MCP tools using slug-derived names.
func registerCycleTools(s *mcpserver.MCPServer, slug, pluralSlug string, cycles *service.CycleService, containment node.ContainmentRepository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_" + pluralSlug,
		Description: "Lists " + pluralSlug + " (sprints) in a project. Returns name, status, and start/end dates for each.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier"},
		},
	}, listCycles(cycles, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_get_" + slug,
		Description: "Fetches a single " + slug + " by cycle_id UUID. Use tack_list_" + pluralSlug + " first to find the ID.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"cycle_id":           map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "cycle_id"},
		},
	}, getCycle(cycles, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_create_" + slug,
		Description: "Creates a new " + slug + " in a project. start_date and end_date use YYYY-MM-DD format.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
				"description":        map[string]any{"type": "string"},
				"start_date":         map[string]any{"type": "string"},
				"end_date":           map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "name"},
		},
	}, createCycle(cycles, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_update_" + slug,
		Description: "Updates fields on a " + slug + " by cycle_id UUID. Only provided fields change.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"cycle_id":           map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
				"description":        map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "cycle_id"},
		},
	}, updateCycle(cycles, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_delete_" + slug,
		Description: "Deletes a " + slug + " by cycle_id UUID. Deletion is permanent and irreversible.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug":     map[string]any{"type": "string"},
				"project_identifier": map[string]any{"type": "string"},
				"cycle_id":           map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "project_identifier", "cycle_id"},
		},
	}, deleteCycle(cycles, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_add_to_" + slug,
		Description: "Adds issues to a " + slug + " by their UUID issue IDs.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
				"cycle_id":       map[string]any{"type": "string"},
				"issue_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			Required: []string{"workspace_slug", "cycle_id", "issue_ids"},
		},
	}, addToCycle(containment, r))
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_remove_from_" + slug,
		Description: "Removes an issue from a " + slug + ".",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
				"cycle_id":       map[string]any{"type": "string"},
				"issue_id":       map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug", "cycle_id", "issue_id"},
		},
	}, removeFromCycle(containment, r))
}

type ListCyclesInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listCycles(cycles *service.CycleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListCyclesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		cs, err := cycles.ListWithWorkspace(ctx, ws.ID, proj.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"cycles": cs}, ""), nil
	}
}

type GetCycleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	CycleID           string `json:"cycle_id"`
}

func getCycle(cycles *service.CycleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetCycleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		c, err := cycles.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, cycleID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"cycle": c}, ""), nil
	}
}

type CreateCycleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	StartDate         *string `json:"start_date,omitempty"`
	EndDate           *string `json:"end_date,omitempty"`
}

func createCycle(cycles *service.CycleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateCycleInput
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
		newCycle := &cycle.Cycle{
			WorkspaceID: ws.ID,
			ProjectID:   proj.ID,
			Name:        in.Name,
			SortOrder:   65535,
			CreatedBy:   userID,
		}
		if in.Description != nil {
			newCycle.Description = *in.Description
		}
		if in.StartDate != nil {
			t, err := parseOptionalDate(*in.StartDate, "start_date")
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			newCycle.StartDate = t
		}
		if in.EndDate != nil {
			t, err := parseOptionalDate(*in.EndDate, "end_date")
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			newCycle.EndDate = t
		}
		c, err := cycles.Create(ctx, newCycle)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"cycle": c}, "Use tack_add_to_cycle to add issues to this cycle."), nil
	}
}

type UpdateCycleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	CycleID           string  `json:"cycle_id"`
	Name              *string `json:"name,omitempty"`
	Description       *string `json:"description,omitempty"`
}

func updateCycle(cycles *service.CycleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in UpdateCycleInput
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
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		existing, err := cycles.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, cycleID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if in.Name != nil {
			existing.Name = *in.Name
		}
		if in.Description != nil {
			existing.Description = *in.Description
		}
		existing.UpdatedBy = &userID
		updated, err := cycles.Update(ctx, existing)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"cycle": updated}, ""), nil
	}
}

type DeleteCycleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	CycleID           string `json:"cycle_id"`
}
type DeleteCycleOutput struct {
	OK bool `json:"ok"`
}

func deleteCycle(cycles *service.CycleService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteCycleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := cycles.DeleteByWorkspace(ctx, ws.ID, proj.ID, cycleID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteCycleOutput{OK: true}, "Deletion is permanent and irreversible."), nil
	}
}

type AddToCycleInput struct {
	WorkspaceSlug string   `json:"workspace_slug"`
	CycleID       string   `json:"cycle_id"`
	IssueIDs      []string `json:"issue_ids"`
}
type AddToCycleOutput struct {
	Added int `json:"added"`
}

func addToCycle(containment node.ContainmentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in AddToCycleInput
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
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		var added int
		for _, s := range in.IssueIDs {
			issueID, err := parseUUID(s, "issue_id")
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			if err := containment.AddIssueToCycle(ctx, ws.OrgID, cycleID, issueID, userID); err != nil {
				return ClassifyError(ctx, err), nil
			}
			added++
		}
		return Success(AddToCycleOutput{Added: added}, ""), nil
	}
}

type RemoveFromCycleInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	CycleID       string `json:"cycle_id"`
	IssueID       string `json:"issue_id"`
}
type RemoveFromCycleOutput struct {
	OK bool `json:"ok"`
}

func removeFromCycle(containment node.ContainmentRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in RemoveFromCycleInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		issueID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := containment.RemoveIssueFromCycle(ctx, ws.OrgID, cycleID, issueID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(RemoveFromCycleOutput{OK: true}, ""), nil
	}
}
