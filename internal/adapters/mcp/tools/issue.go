package tools

import (
	"context"
	"encoding/json"
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
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	StateID     string `json:"state_id"    `
	Priority    string `json:"priority"    `
	EpicID      string `json:"epic_id"     `
	AssigneeID  string `json:"assignee_id" `
}
type ListIssuesOutput struct {
	Issues json.RawMessage `json:"issues"`
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
		if id := parseOptionalUUID(in.StateID); id != nil {
			filter.StateIDs = []uuid.UUID{*id}
		}
		if id := parseOptionalUUID(in.EpicID); id != nil {
			filter.EpicID = id
		}
		if id := parseOptionalUUID(in.AssigneeID); id != nil {
			filter.AssigneeIDs = []uuid.UUID{*id}
		}
		if in.Priority != "" {
			filter.Priorities = []issue.Priority{issue.Priority(in.Priority)}
		}
		items, total, err := svc.List(ctx, filter)
		if err != nil {
			return nil, ListIssuesOutput{}, err
		}
		b, _ := json.Marshal(items)
		return nil, ListIssuesOutput{Issues: b, Total: total}, nil
	}
}

// ── get ──────────────────────────────────────────────────────────────────────

type GetIssueInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	IssueID     string `json:"issue_id"     jsonschema:"required"`
}
type GetIssueOutput struct {
	Issue json.RawMessage `json:"issue"`
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
		b, _ := json.Marshal(item)
		return nil, GetIssueOutput{Issue: b}, nil
	}
}

// ── create ───────────────────────────────────────────────────────────────────

type CreateIssueInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	Name        string `json:"name"         jsonschema:"required"`
	Description string `json:"description" `
	Priority    string `json:"priority"    `
	StateID     string `json:"state_id"    `
	EpicID      string `json:"epic_id"     `
}
type CreateIssueOutput struct {
	Issue json.RawMessage `json:"issue"`
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
			Description: in.Description,
			Priority:    issue.Priority(in.Priority),
			StateID:     parseOptionalUUID(in.StateID),
			EpicID:      parseOptionalUUID(in.EpicID),
		}
		i.CreatedBy = userID
		created, err := svc.Create(ctx, i)
		if err != nil {
			return nil, CreateIssueOutput{}, fmt.Errorf("create issue: %w", err)
		}
		b, _ := json.Marshal(created)
		return nil, CreateIssueOutput{Issue: b}, nil
	}
}

// ── update (partial) ─────────────────────────────────────────────────────────

type UpdateIssueInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	IssueID     string `json:"issue_id"     jsonschema:"required"`
	Name        string `json:"name"        `
	Description string `json:"description" `
	Priority    string `json:"priority"    `
	EpicID      string `json:"epic_id"     `
}
type UpdateIssueOutput struct {
	Issue json.RawMessage `json:"issue"`
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
		if in.Name != "" {
			existing.Name = in.Name
		}
		if in.Description != "" {
			existing.Description = in.Description
		}
		if in.Priority != "" {
			existing.Priority = issue.Priority(in.Priority)
		}
		if in.EpicID != "" {
			existing.EpicID = parseOptionalUUID(in.EpicID)
		}
		existing.UpdatedBy = &userID
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, UpdateIssueOutput{}, err
		}
		b, _ := json.Marshal(updated)
		return nil, UpdateIssueOutput{Issue: b}, nil
	}
}

// ── delete ───────────────────────────────────────────────────────────────────

type DeleteIssueInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	IssueID     string `json:"issue_id"     jsonschema:"required"`
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
	WorkspaceID string   `json:"workspace_id"  jsonschema:"required"`
	ProjectID   string   `json:"project_id"    jsonschema:"required"`
	IssueID     string   `json:"issue_id"      jsonschema:"required"`
	AssigneeIDs []string `json:"assignee_ids"  jsonschema:"required"`
}
type AssignIssueOutput struct {
	Issue json.RawMessage `json:"issue"`
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
		b, _ := json.Marshal(updated)
		return nil, AssignIssueOutput{Issue: b}, nil
	}
}

// ── set state ────────────────────────────────────────────────────────────────

type SetIssueStateInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"   jsonschema:"required"`
	IssueID     string `json:"issue_id"     jsonschema:"required"`
	StateID     string `json:"state_id"     jsonschema:"required"`
}
type SetIssueStateOutput struct {
	Issue json.RawMessage `json:"issue"`
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
		b, _ := json.Marshal(updated)
		return nil, SetIssueStateOutput{Issue: b}, nil
	}
}

// ── move ─────────────────────────────────────────────────────────────────────

type MoveIssueInput struct {
	WorkspaceID      string `json:"workspace_id"       jsonschema:"required"`
	ProjectID        string `json:"project_id"         jsonschema:"required"`
	IssueID          string `json:"issue_id"           jsonschema:"required"`
	TargetProjectID  string `json:"target_project_id"  jsonschema:"required"`
}
type MoveIssueOutput struct {
	Issue json.RawMessage `json:"issue"`
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
		b, _ := json.Marshal(updated)
		return nil, MoveIssueOutput{Issue: b}, nil
	}
}

// ── bulk update ──────────────────────────────────────────────────────────────

type BulkUpdateIssuesInput struct {
	WorkspaceID string   `json:"workspace_id" jsonschema:"required"`
	ProjectID   string   `json:"project_id"   jsonschema:"required"`
	IssueIDs    []string `json:"issue_ids"    jsonschema:"required"`
	StateID     string   `json:"state_id"    `
	Priority    string   `json:"priority"    `
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
			if in.StateID != "" {
				if sID := parseOptionalUUID(in.StateID); sID != nil {
					existing.StateID = sID
				}
			}
			if in.Priority != "" {
				existing.Priority = issue.Priority(in.Priority)
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
