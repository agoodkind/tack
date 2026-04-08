package tools

import (
	"context"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterIssue(s *mcp.Server, svc issue.Service) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_issues", Description: "List issues in a project with optional filters"}, listIssues(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_issue", Description: "Get a single issue including its description"}, getIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_issue", Description: "Create a new issue"}, createIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_issue", Description: "Update issue fields (partial — only provided fields are changed)"}, updateIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_issue", Description: "Soft-delete an issue"}, deleteIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_assign_issue", Description: "Set the assignees on an issue (replaces existing assignees)"}, assignIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_set_issue_state", Description: "Move an issue to a new workflow state"}, setIssueState(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_move_issue", Description: "Move an issue to a different project"}, moveIssue(svc))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_update_issues", Description: "Update multiple issues at once (partial — only provided fields are changed)"}, bulkUpdateIssues(svc))
}

// ── list ─────────────────────────────────────────────────────────────────────

type ListIssuesInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	StateID     *string `json:"state_id,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
	AssigneeID  *string `json:"assignee_id,omitempty"`
}
type ListIssuesOutput struct {
	Issues any `json:"issues"`
	Total  int             `json:"total"`
}

func listIssues(svc issue.Service) mcp.ToolHandlerFor[ListIssuesInput, ListIssuesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListIssuesInput) (*mcp.CallToolResult, ListIssuesOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, ListIssuesOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, ListIssuesOutput{}, err
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
			return nil, ListIssuesOutput{}, err
		}
		return nil, ListIssuesOutput{Issues: items, Total: total}, nil
	}
}

// ── get ──────────────────────────────────────────────────────────────────────

type GetIssueInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"` 
	IssueID     string `json:"issue_id"` 
}
type GetIssueOutput struct {
	Issue any `json:"issue"`
}

func getIssue(svc issue.Service) mcp.ToolHandlerFor[GetIssueInput, GetIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetIssueInput) (*mcp.CallToolResult, GetIssueOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, GetIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, GetIssueOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, GetIssueOutput{}, err
		}
		item, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, GetIssueOutput{}, err
		}
		return nil, GetIssueOutput{Issue: item}, nil
	}
}

// ── create ───────────────────────────────────────────────────────────────────

type CreateIssueInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	StateID     *string `json:"state_id,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
}
type CreateIssueOutput struct {
	Issue any `json:"issue"`
}

func createIssue(svc issue.Service) mcp.ToolHandlerFor[CreateIssueInput, CreateIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateIssueInput) (*mcp.CallToolResult, CreateIssueOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, CreateIssueOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, CreateIssueOutput{}, err
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
			return nil, CreateIssueOutput{}, fmt.Errorf("create issue: %w", err)
		}
		return nil, CreateIssueOutput{Issue: created}, nil
	}
}

// ── update (partial) ─────────────────────────────────────────────────────────

type UpdateIssueInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	IssueID     string  `json:"issue_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	EpicID      *string `json:"epic_id,omitempty"`
}
type UpdateIssueOutput struct {
	Issue any `json:"issue"`
}

func updateIssue(svc issue.Service) mcp.ToolHandlerFor[UpdateIssueInput, UpdateIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateIssueInput) (*mcp.CallToolResult, UpdateIssueOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, UpdateIssueOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, UpdateIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateIssueOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, UpdateIssueOutput{}, err
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, UpdateIssueOutput{}, err
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
			return nil, UpdateIssueOutput{}, err
		}
		return nil, UpdateIssueOutput{Issue: updated}, nil
	}
}

// ── delete ───────────────────────────────────────────────────────────────────

type DeleteIssueInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"` 
	IssueID     string `json:"issue_id"` 
}
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

type AssignIssueInput struct {
	WorkspaceID string   `json:"workspace_id,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	IssueID     string   `json:"issue_id,omitempty"`
	AssigneeIDs []string `json:"assignee_ids,omitempty"`
}
type AssignIssueOutput struct {
	Issue any `json:"issue"`
}

func assignIssue(svc issue.Service) mcp.ToolHandlerFor[AssignIssueInput, AssignIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AssignIssueInput) (*mcp.CallToolResult, AssignIssueOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		assigneeIDs := make([]uuid.UUID, 0, len(in.AssigneeIDs))
		for _, s := range in.AssigneeIDs {
			id, err := parseUUID(s, "assignee_id")
			if err != nil {
				return nil, AssignIssueOutput{}, err
			}
			assigneeIDs = append(assigneeIDs, id)
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		existing.AssigneeIDs = assigneeIDs
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, AssignIssueOutput{}, err
		}
		return nil, AssignIssueOutput{Issue: updated}, nil
	}
}

// ── set state ────────────────────────────────────────────────────────────────

type SetIssueStateInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"` 
	IssueID     string `json:"issue_id"` 
	StateID     string `json:"state_id"` 
}
type SetIssueStateOutput struct {
	Issue any `json:"issue"`
}

func setIssueState(svc issue.Service) mcp.ToolHandlerFor[SetIssueStateInput, SetIssueStateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetIssueStateInput) (*mcp.CallToolResult, SetIssueStateOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		existing.StateID = &sID
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, SetIssueStateOutput{}, err
		}
		return nil, SetIssueStateOutput{Issue: updated}, nil
	}
}

// ── move ─────────────────────────────────────────────────────────────────────

type MoveIssueInput struct {
	WorkspaceID      string `json:"workspace_id"` 
	ProjectID        string `json:"project_id"` 
	IssueID          string `json:"issue_id"` 
	TargetProjectID  string `json:"target_project_id"` 
}
type MoveIssueOutput struct {
	Issue any `json:"issue"`
}

func moveIssue(svc issue.Service) mcp.ToolHandlerFor[MoveIssueInput, MoveIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in MoveIssueInput) (*mcp.CallToolResult, MoveIssueOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		iID, err := parseUUID(in.IssueID, "issue_id")
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		targetPID, err := parseUUID(in.TargetProjectID, "target_project_id")
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		existing, err := svc.Get(ctx, wsID, pID, iID)
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		existing.ProjectID = targetPID
		existing.StateID = nil // state may not exist in target project
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, MoveIssueOutput{}, err
		}
		return nil, MoveIssueOutput{Issue: updated}, nil
	}
}

// ── bulk update ──────────────────────────────────────────────────────────────

type BulkUpdateIssuesInput struct {
	WorkspaceID string   `json:"workspace_id"`
	ProjectID   string   `json:"project_id,omitempty"`
	IssueIDs    []string `json:"issue_ids,omitempty"`
	StateID     *string  `json:"state_id,omitempty"`
	Priority    *string  `json:"priority,omitempty"`
	AssigneeIDs []string `json:"assignee_ids"`
}
type BulkUpdateIssuesOutput struct {
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
}

func bulkUpdateIssues(svc issue.Service) mcp.ToolHandlerFor[BulkUpdateIssuesInput, BulkUpdateIssuesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in BulkUpdateIssuesInput) (*mcp.CallToolResult, BulkUpdateIssuesOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, BulkUpdateIssuesOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, BulkUpdateIssuesOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, BulkUpdateIssuesOutput{}, err
		}
		assigneeIDs := make([]uuid.UUID, 0, len(in.AssigneeIDs))
		for _, s := range in.AssigneeIDs {
			id, err := parseUUID(s, "assignee_id")
			if err != nil {
				return nil, BulkUpdateIssuesOutput{}, err
			}
			assigneeIDs = append(assigneeIDs, id)
		}
		var updated, failed int
		for _, rawID := range in.IssueIDs {
			iID, err := parseUUID(rawID, "issue_id")
			if err != nil {
				failed++
				continue
			}
			existing, err := svc.Get(ctx, wsID, pID, iID)
			if err != nil {
				failed++
				continue
			}
			if in.StateID != nil {
				if sID := parseOptionalUUID(*in.StateID); sID != nil {
					existing.StateID = sID
				}
			}
			if in.Priority != nil {
				existing.Priority = issue.Priority(*in.Priority)
			}
			if len(assigneeIDs) > 0 {
				existing.AssigneeIDs = assigneeIDs
			}
			existing.UpdatedBy = &userID
			if _, err := svc.Update(ctx, existing); err != nil {
				failed++
			} else {
				updated++
			}
		}
		return nil, BulkUpdateIssuesOutput{Updated: updated, Failed: failed}, nil
	}
}
