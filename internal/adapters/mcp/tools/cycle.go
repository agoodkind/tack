package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterCycle(s *mcp.Server, cycles cycle.Repository, containment node.ContainmentRepository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_cycles", Description: "List cycles (sprints) in a project"}, listCycles(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_cycle", Description: "Get a cycle by ID"}, getCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_cycle", Description: "Create a new cycle"}, createCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_cycle", Description: "Update cycle fields (partial)"}, updateCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_cycle", Description: "Delete a cycle"}, deleteCycle(cycles))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_add_to_cycle", Description: "Add issues to a cycle"}, addToCycle(containment))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_remove_from_cycle", Description: "Remove an issue from a cycle"}, removeFromCycle(containment))
}

type ListCyclesInput struct {
	ProjectID string `json:"project_id"`
}

func listCycles(cycles cycle.Repository) mcp.ToolHandlerFor[ListCyclesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListCyclesInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		cs, err := cycles.List(ctx, pID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"cycles": cs}, nil
	}
}

type GetCycleInput struct {
	ProjectID string `json:"project_id"`
	CycleID   string `json:"cycle_id"`
}

func getCycle(cycles cycle.Repository) mcp.ToolHandlerFor[GetCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetCycleInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, nil, err
		}
		c, err := cycles.GetByID(ctx, pID, cID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"cycle": c}, nil
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

func createCycle(cycles cycle.Repository) mcp.ToolHandlerFor[CreateCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateCycleInput) (*mcp.CallToolResult, any, error) {
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
				return nil, nil, err
			}
			newCycle.StartDate = t
		}
		if in.EndDate != nil {
			t, err := parseOptionalDate(*in.EndDate, "end_date")
			if err != nil {
				return nil, nil, err
			}
			newCycle.EndDate = t
		}
		c, err := cycles.Create(ctx, newCycle)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"cycle": c}, nil
	}
}

type UpdateCycleInput struct {
	ProjectID   string  `json:"project_id"`
	CycleID     string  `json:"cycle_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func updateCycle(cycles cycle.Repository) mcp.ToolHandlerFor[UpdateCycleInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateCycleInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := cycles.GetByID(ctx, pID, cID)
		if err != nil {
			return nil, nil, err
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
			return nil, nil, err
		}
		return nil, map[string]any{"cycle": updated}, nil
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
	OrgID    string   `json:"org_id"`
	CycleID  string   `json:"cycle_id"`
	IssueIDs []string `json:"issue_ids"`
}
type AddToCycleOutput struct {
	Added int `json:"added"`
}

func addToCycle(containment node.ContainmentRepository) mcp.ToolHandlerFor[AddToCycleInput, AddToCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddToCycleInput) (*mcp.CallToolResult, AddToCycleOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, AddToCycleOutput{}, err
		}
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, AddToCycleOutput{}, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, AddToCycleOutput{}, err
		}
		var added int
		for _, s := range in.IssueIDs {
			iID, err := parseUUID(s, "issue_id")
			if err != nil {
				return nil, AddToCycleOutput{}, err
			}
			if err := containment.AddIssueToCycle(ctx, orgID, cID, iID, userID); err != nil {
				return nil, AddToCycleOutput{}, err
			}
			added++
		}
		return nil, AddToCycleOutput{Added: added}, nil
	}
}

type RemoveFromCycleInput struct {
	OrgID   string `json:"org_id"`
	CycleID string `json:"cycle_id"`
	IssueID string `json:"issue_id"`
}
type RemoveFromCycleOutput struct {
	OK bool `json:"ok"`
}

func removeFromCycle(containment node.ContainmentRepository) mcp.ToolHandlerFor[RemoveFromCycleInput, RemoveFromCycleOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveFromCycleInput) (*mcp.CallToolResult, RemoveFromCycleOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		cID, err := parseUUID(in.CycleID, "cycle_id")
		if err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		if err := containment.RemoveIssueFromCycle(ctx, orgID, cID, iID); err != nil {
			return nil, RemoveFromCycleOutput{}, err
		}
		return nil, RemoveFromCycleOutput{OK: true}, nil
	}
}
