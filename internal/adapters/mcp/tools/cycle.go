package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/cycle"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCycleTools registers all cycle-related MCP tools using slug-derived names.
func registerCycleTools(s *mcp.Server, slug, pluralSlug string, cycles *service.CycleService, containment node.ContainmentRepository, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_" + pluralSlug, Description: "Lists " + pluralSlug + " (sprints) in a project. Returns name, status, and start/end dates for each."}, listCycles(cycles, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_" + slug, Description: "Fetches a single " + slug + " by cycle_id UUID. Use tack_list_" + pluralSlug + " first to find the ID."}, getCycle(cycles, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_" + slug, Description: "Creates a new " + slug + " in a project. start_date and end_date use YYYY-MM-DD format."}, createCycle(cycles, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_" + slug, Description: "Updates fields on a " + slug + " by cycle_id UUID. Only provided fields change."}, updateCycle(cycles, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_" + slug, Description: "Deletes a " + slug + " by cycle_id UUID. Deletion is permanent and irreversible."}, deleteCycle(cycles, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_" + slug, Description: "Adds issues to a " + slug + " by their UUID issue IDs."}, addToCycle(containment, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_" + slug, Description: "Removes an issue from a " + slug + "."}, removeFromCycle(containment, r))
}

type ListCyclesInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listCycles(cycles *service.CycleService, r *Resolver) mcp.ToolHandlerFor[ListCyclesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListCyclesInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		cs, err := cycles.ListWithWorkspace(ctx, ws.ID, proj.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"cycles": cs}, ""), nil, nil
	}
}

type GetCycleInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
	CycleID           string `json:"cycle_id"`
}

func getCycle(cycles *service.CycleService, r *Resolver) mcp.ToolHandlerFor[GetCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetCycleInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		c, err := cycles.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, cycleID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"cycle": c}, ""), nil, nil
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

func createCycle(cycles *service.CycleService, r *Resolver) mcp.ToolHandlerFor[CreateCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateCycleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
				return RecoverableError(err.Error()), nil, nil
			}
			newCycle.StartDate = t
		}
		if in.EndDate != nil {
			t, err := parseOptionalDate(*in.EndDate, "end_date")
			if err != nil {
				return RecoverableError(err.Error()), nil, nil
			}
			newCycle.EndDate = t
		}
		c, err := cycles.Create(ctx, newCycle)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"cycle": c}, "Use tack_add_to_cycle to add issues to this cycle."), nil, nil
	}
}

type UpdateCycleInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	CycleID           string  `json:"cycle_id"`
	Name              *string `json:"name,omitempty"`
	Description       *string `json:"description,omitempty"`
}

func updateCycle(cycles *service.CycleService, r *Resolver) mcp.ToolHandlerFor[UpdateCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateCycleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		existing, err := cycles.GetByIDWithWorkspace(ctx, ws.ID, proj.ID, cycleID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"cycle": updated}, ""), nil, nil
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

func deleteCycle(cycles *service.CycleService, r *Resolver) mcp.ToolHandlerFor[DeleteCycleInput, DeleteCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteCycleInput) (*mcp.CallToolResult, DeleteCycleOutput, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), DeleteCycleOutput{}, nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), DeleteCycleOutput{}, nil
		}
		if err := cycles.DeleteByWorkspace(ctx, ws.ID, proj.ID, cycleID); err != nil {
			return ClassifyError(ctx, err), DeleteCycleOutput{}, nil
		}
		return Success(DeleteCycleOutput{OK: true}, "Deletion is permanent and irreversible."), DeleteCycleOutput{}, nil
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

func addToCycle(containment node.ContainmentRepository, r *Resolver) mcp.ToolHandlerFor[AddToCycleInput, AddToCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToCycleInput) (*mcp.CallToolResult, AddToCycleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), AddToCycleOutput{}, nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), AddToCycleOutput{}, nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), AddToCycleOutput{}, nil
		}
		var added int
		for _, s := range in.IssueIDs {
			issueID, err := parseUUID(s, "issue_id")
			if err != nil {
				return RecoverableError(err.Error()), AddToCycleOutput{}, nil
			}
			if err := containment.AddIssueToCycle(ctx, ws.OrgID, cycleID, issueID, userID); err != nil {
				return ClassifyError(ctx, err), AddToCycleOutput{}, nil
			}
			added++
		}
		return Success(AddToCycleOutput{Added: added}, ""), AddToCycleOutput{}, nil
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

func removeFromCycle(containment node.ContainmentRepository, r *Resolver) mcp.ToolHandlerFor[RemoveFromCycleInput, RemoveFromCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromCycleInput) (*mcp.CallToolResult, RemoveFromCycleOutput, error) {
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), RemoveFromCycleOutput{}, nil
		}
		cycleID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return RecoverableError(err.Error()), RemoveFromCycleOutput{}, nil
		}
		issueID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return RecoverableError(err.Error()), RemoveFromCycleOutput{}, nil
		}
		if err := containment.RemoveIssueFromCycle(ctx, ws.OrgID, cycleID, issueID); err != nil {
			return ClassifyError(ctx, err), RemoveFromCycleOutput{}, nil
		}
		return Success(RemoveFromCycleOutput{OK: true}, ""), RemoveFromCycleOutput{}, nil
	}
}
