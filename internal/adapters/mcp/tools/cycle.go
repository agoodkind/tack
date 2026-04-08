package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterCycle(s *mcp.Server, cycles cycle.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_cycles", Description: "List cycles (sprints) in a project"}, listCycles(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_cycle", Description: "Get a cycle by ID"}, getCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_cycle", Description: "Create a new cycle"}, createCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_cycle", Description: "Update cycle fields (partial)"}, updateCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_cycle", Description: "Delete a cycle"}, deleteCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_cycle", Description: "Add issues to a cycle"}, addToCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_cycle", Description: "Remove an issue from a cycle"}, removeFromCycle(cycles))
}

type ListCyclesInput struct {
	ProjectID string `json:"project_id"`
}
type ListCyclesOutput struct {
	Cycles any `json:"cycles"`
}

func listCycles(cycles cycle.Repository) mcp.ToolHandlerFor[ListCyclesInput, ListCyclesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListCyclesInput) (*mcp.CallToolResult, ListCyclesOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, ListCyclesOutput{}, err
		}
		cs, err := cycles.List(ctx, pID)
		if err != nil {
			return nil, ListCyclesOutput{}, err
		}
		return nil, ListCyclesOutput{Cycles: cs}, nil
	}
}

type GetCycleInput struct {
	ProjectID string `json:"project_id"`
	CycleID   string `json:"cycle_id"` 
}
type GetCycleOutput struct {
	Cycle any `json:"cycle"`
}

func getCycle(cycles cycle.Repository) mcp.ToolHandlerFor[GetCycleInput, GetCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetCycleInput) (*mcp.CallToolResult, GetCycleOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, GetCycleOutput{}, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, GetCycleOutput{}, err
		}
		c, err := cycles.GetByID(ctx, pID, cID)
		if err != nil {
			return nil, GetCycleOutput{}, err
		}
		return nil, GetCycleOutput{Cycle: c}, nil
	}
}

type CreateCycleInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	StartDate   *string `json:"start_date,omitempty"`
	EndDate     *string `json:"end_date,omitempty"`
}
type CreateCycleOutput struct {
	Cycle any `json:"cycle"`
}

func createCycle(cycles cycle.Repository) mcp.ToolHandlerFor[CreateCycleInput, CreateCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateCycleInput) (*mcp.CallToolResult, CreateCycleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, CreateCycleOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateCycleOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, CreateCycleOutput{}, err
		}
		newCycle := &cycle.Cycle{
			WorkspaceID: wsID,
			ProjectID:   pID,
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
				return nil, CreateCycleOutput{}, err
			}
			newCycle.StartDate = t
		}
		if in.EndDate != nil {
			t, err := parseOptionalDate(*in.EndDate, "end_date")
			if err != nil {
				return nil, CreateCycleOutput{}, err
			}
			newCycle.EndDate = t
		}
		c, err := cycles.Create(ctx, newCycle)
		if err != nil {
			return nil, CreateCycleOutput{}, err
		}
		return nil, CreateCycleOutput{Cycle: c}, nil
	}
}

type UpdateCycleInput struct {
	ProjectID   string  `json:"project_id"`
	CycleID     string  `json:"cycle_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}
type UpdateCycleOutput struct {
	Cycle any `json:"cycle"`
}

func updateCycle(cycles cycle.Repository) mcp.ToolHandlerFor[UpdateCycleInput, UpdateCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateCycleInput) (*mcp.CallToolResult, UpdateCycleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, UpdateCycleOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateCycleOutput{}, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, UpdateCycleOutput{}, err
		}
		existing, err := cycles.GetByID(ctx, pID, cID)
		if err != nil {
			return nil, UpdateCycleOutput{}, err
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
			return nil, UpdateCycleOutput{}, err
		}
		return nil, UpdateCycleOutput{Cycle: updated}, nil
	}
}

type DeleteCycleInput struct {
	CycleID string `json:"cycle_id"`
}
type DeleteCycleOutput struct {
	OK bool `json:"ok"`
}

func deleteCycle(cycles cycle.Repository) mcp.ToolHandlerFor[DeleteCycleInput, DeleteCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteCycleInput) (*mcp.CallToolResult, DeleteCycleOutput, error) {
		id, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, DeleteCycleOutput{}, err
		}
		if err := cycles.Delete(ctx, id); err != nil {
			return nil, DeleteCycleOutput{}, err
		}
		return nil, DeleteCycleOutput{OK: true}, nil
	}
}

type AddToCycleInput struct {
	CycleID  string   `json:"cycle_id,omitempty"`
	IssueIDs []string `json:"issue_ids,omitempty"`
}
type AddToCycleOutput struct {
	Added int `json:"added"`
}

func addToCycle(cycles cycle.Repository) mcp.ToolHandlerFor[AddToCycleInput, AddToCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToCycleInput) (*mcp.CallToolResult, AddToCycleOutput, error) {
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, AddToCycleOutput{}, err
		}
		ids := make([]uuid.UUID, 0, len(in.IssueIDs))
		for _, s := range in.IssueIDs {
			id, err := parseUUID(s, "issue_id")
			if err != nil {
				return nil, AddToCycleOutput{}, err
			}
			ids = append(ids, id)
		}
		if err := cycles.AddIssues(ctx, cID, ids); err != nil {
			return nil, AddToCycleOutput{}, err
		}
		return nil, AddToCycleOutput{Added: len(ids)}, nil
	}
}

type RemoveFromCycleInput struct {
	CycleID string `json:"cycle_id"` 
	IssueID string `json:"issue_id"` 
}
type RemoveFromCycleOutput struct {
	OK bool `json:"ok"`
}

func removeFromCycle(cycles cycle.Repository) mcp.ToolHandlerFor[RemoveFromCycleInput, RemoveFromCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromCycleInput) (*mcp.CallToolResult, RemoveFromCycleOutput, error) {
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		if err := cycles.RemoveIssue(ctx, cID, iID); err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		return nil, RemoveFromCycleOutput{OK: true}, nil
	}
}
