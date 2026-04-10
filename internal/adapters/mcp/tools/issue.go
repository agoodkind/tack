package tools

import (
	"context"
	"fmt"

	"goodkind.io/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterIssue registers all issue-related MCP tools on the given server.
func RegisterIssue(s *mcp.Server, svc issue.Service) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_issues", Description: "List issues in a project with optional filters"}, listIssues(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_issue", Description: "Get a single issue including its description"}, getIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_issue", Description: "Create a new issue"}, createIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_issue", Description: "Update issue fields (partial — only provided fields are changed)"}, updateIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_issue", Description: "Soft-delete an issue"}, deleteIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_assign_issue", Description: "Set the assignees on an issue (replaces existing assignees)"}, assignIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_set_issue_state", Description: "Move an issue to a new workflow state"}, setIssueState(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_move_issue", Description: "Move an issue to a different project (reallocates sequence ID)"}, moveIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_update_issues", Description: "Apply the same field changes to multiple issues in one operation"}, bulkUpdateIssues(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_delete_issues", Description: "Soft-delete multiple issues in one operation"}, bulkDeleteIssues(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_move_issues", Description: "Move multiple issues to a different project in one operation"}, bulkMoveIssues(svc))
}

// ── list ─────────────────────────────────────────────────────────────────────

// ListIssuesInput holds the parameters for listing issues with optional filters.
type ListIssuesInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	StateID     *string `json:"state_id,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
	AssigneeID  *string `json:"assignee_id,omitempty"`
}

func listIssues(svc issue.Service) mcp.ToolHandlerFor[ListIssuesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListIssuesInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		filter := issue.ListFilter{WorkspaceID: wsID, ProjectID: &pID}
		if in.StateID != nil {
			if id := parseOptionalUUID(*in.StateID); id != nil {
				filter.StateIDs = []uuid.UUID{*id}
			}
		}
		if in.EpicID != nil {
			if id := parseOptionalUUID(*in.EpicID); id != nil {
				filter.EpicID = id
			}
		}
		if in.AssigneeID != nil {
			if id := parseOptionalUUID(*in.AssigneeID); id != nil {
				filter.AssigneeIDs = []uuid.UUID{*id}
			}
		}
		if in.Priority != nil {
			filter.Priorities = []issue.Priority{issue.Priority(*in.Priority)}
		}
		items, total, err := svc.List(ctx, filter)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issues": items, "total": total}, nil
	}
}

// ── get ──────────────────────────────────────────────────────────────────────

// GetIssueInput holds the parameters for fetching a single issue.
type GetIssueInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	IssueID     string `json:"issue_id"`
}

func getIssue(svc issue.Service) mcp.ToolHandlerFor[GetIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetIssueInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, nil, err
		}
		item, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": item}, nil
	}
}

// ── create ───────────────────────────────────────────────────────────────────

// CreateIssueInput holds the parameters for creating a new issue.
type CreateIssueInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	StateID     *string `json:"state_id,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
}

func createIssue(svc issue.Service) mcp.ToolHandlerFor[CreateIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateIssueInput) (*mcp.CallToolResult, any, error) {
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
		i := &issue.Issue{
			WorkspaceID: wsID,
			ProjectID:   pID,
			Name:        in.Name,
		}
		if in.Description != nil {
			i.Description = *in.Description
		}
		if in.Priority != nil {
			i.Priority = issue.Priority(*in.Priority)
		}
		if in.StateID != nil {
			i.StateID = parseOptionalUUID(*in.StateID)
		}
		if in.EpicID != nil {
			i.EpicID = parseOptionalUUID(*in.EpicID)
		}
		i.CreatedBy = userID
		created, err := svc.Create(ctx, i)
		if err != nil {
			return nil, nil, fmt.Errorf("create issue: %w", err)
		}
		return nil, map[string]any{"issue": created}, nil
	}
}

// ── update (partial) ─────────────────────────────────────────────────────────

// UpdateIssueInput holds the parameters for a partial issue update.
type UpdateIssueInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	IssueID     string  `json:"issue_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
}

func updateIssue(svc issue.Service) mcp.ToolHandlerFor[UpdateIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateIssueInput) (*mcp.CallToolResult, any, error) {
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
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, nil, err
		}
		if in.Name != nil {
			existing.Name = *in.Name
		}
		if in.Description != nil {
			existing.Description = *in.Description
		}
		if in.Priority != nil {
			existing.Priority = issue.Priority(*in.Priority)
		}
		if in.EpicID != nil {
			existing.EpicID = parseOptionalUUID(*in.EpicID)
		}
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": updated}, nil
	}
}

// ── delete ───────────────────────────────────────────────────────────────────

// DeleteIssueInput holds the parameters for soft-deleting an issue.
type DeleteIssueInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	IssueID     string `json:"issue_id"`
}

// DeleteIssueOutput reports whether the deletion succeeded.
type DeleteIssueOutput struct {
	OK bool `json:"ok"`
}

func deleteIssue(svc issue.Service) mcp.ToolHandlerFor[DeleteIssueInput, DeleteIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteIssueInput) (*mcp.CallToolResult, DeleteIssueOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		if err := svc.Delete(ctx, wsID, pID, iID); err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		return nil, DeleteIssueOutput{OK: true}, nil
	}
}

// ── assign ───────────────────────────────────────────────────────────────────

// AssignIssueInput holds the parameters for replacing an issue's assignee set.
type AssignIssueInput struct {
	WorkspaceID string   `json:"workspace_id,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	IssueID     string   `json:"issue_id,omitempty"`
	AssigneeIDs []string `json:"assignee_ids,omitempty"`
}

func assignIssue(svc issue.Service) mcp.ToolHandlerFor[AssignIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AssignIssueInput) (*mcp.CallToolResult, any, error) {
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
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, nil, err
		}
		assigneeIDs := make([]uuid.UUID, 0, len(in.AssigneeIDs))
		for _, s := range in.AssigneeIDs {
			id, parseErr := parseUUID(s, "assignee_id")
			if parseErr != nil {
				return nil, nil, parseErr
			}
			assigneeIDs = append(assigneeIDs, id)
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, nil, err
		}
		existing.AssigneeIDs = assigneeIDs
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": updated}, nil
	}
}

// ── set state ────────────────────────────────────────────────────────────────

// SetIssueStateInput holds the parameters for moving an issue to a new workflow state.
type SetIssueStateInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	IssueID     string `json:"issue_id"`
	StateID     string `json:"state_id"`
}

func setIssueState(svc issue.Service) mcp.ToolHandlerFor[SetIssueStateInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetIssueStateInput) (*mcp.CallToolResult, any, error) {
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
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, nil, err
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, nil, err
		}
		existing.StateID = &sID
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": updated}, nil
	}
}
