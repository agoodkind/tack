package tools

import (
	"context"
	"fmt"

	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerIssueTools registers all issue-related MCP tools using slug-derived names.
// slug is the entity slug (e.g. "issue"); pluralSlug is the plural form (e.g. "issues").
// Both come from the NodeType record -- no hardcoded names.
func registerIssueTools(s *mcp.Server, slug, pluralSlug string, svc *service.IssueService, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_" + pluralSlug, Description: "List " + pluralSlug + " in a project with optional filters"}, listIssues(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_" + slug, Description: "Get a single " + slug + " including its description"}, getIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_" + slug, Description: "Create a new " + slug}, createIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_" + slug, Description: "Update " + slug + " fields (partial -- only provided fields are changed)"}, updateIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_" + slug, Description: "Soft-delete a " + slug}, deleteIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_assign_" + slug, Description: "Set the assignees on a " + slug + " (replaces existing assignees)"}, assignIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_set_" + slug + "_state", Description: "Move a " + slug + " to a new workflow state"}, setIssueState(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_move_" + slug, Description: "Move a " + slug + " to a different project (reallocates sequence ID)"}, moveIssue(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_update_" + pluralSlug, Description: "Apply the same field changes to multiple " + pluralSlug + " in one operation"}, bulkUpdateIssues(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_delete_" + pluralSlug, Description: "Soft-delete multiple " + pluralSlug + " in one operation"}, bulkDeleteIssues(svc, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_bulk_move_" + pluralSlug, Description: "Move multiple " + pluralSlug + " to a different project in one operation"}, bulkMoveIssues(svc, r))
}

// ── list ─────────────────────────────────────────────────────────────────────

// ListIssuesInput holds the parameters for listing issues with optional filters.
type ListIssuesInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier *string `json:"project_identifier,omitempty"`
	StateID           *string `json:"state_id,omitempty"`
	Priority          *string `json:"priority,omitempty"`
	EpicID            *string `json:"epic_id,omitempty"`
	AssigneeID        *string `json:"assignee_id,omitempty"`
}

func listIssues(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[ListIssuesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListIssuesInput) (*mcp.CallToolResult, any, error) {
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, nil, err
		}
		filter := issue.ListFilter{WorkspaceID: ws.ID}
		if in.ProjectIdentifier != nil {
			_, proj, err := r.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if err != nil {
				return nil, nil, err
			}
			filter.ProjectID = &proj.ID
		}
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
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}

func getIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[GetIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetIssueInput) (*mcp.CallToolResult, any, error) {
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		item, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": item}, nil
	}
}

// ── create ───────────────────────────────────────────────────────────────────

// CreateIssueInput holds the parameters for creating a new issue.
type CreateIssueInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Priority          *string `json:"priority,omitempty"`
	StateID           *string `json:"state_id,omitempty"`
	EpicID            *string `json:"epic_id,omitempty"`
}

func createIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[CreateIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateIssueInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		i := &issue.Issue{
			WorkspaceID: ws.ID,
			ProjectID:   proj.ID,
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
	WorkspaceSlug string  `json:"workspace_slug"`
	Identifier    string  `json:"identifier"`
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	Priority      *string `json:"priority,omitempty"`
	EpicID        *string `json:"epic_id,omitempty"`
}

func updateIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[UpdateIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateIssueInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		existing, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
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
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}

// DeleteIssueOutput reports whether the deletion succeeded.
type DeleteIssueOutput struct {
	OK bool `json:"ok"`
}

func deleteIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[DeleteIssueInput, DeleteIssueOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteIssueInput) (*mcp.CallToolResult, DeleteIssueOutput, error) {
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		existing, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		if err := svc.Delete(ctx, ws.ID, proj.ID, existing.ID); err != nil {
			return nil, DeleteIssueOutput{}, err
		}
		return nil, DeleteIssueOutput{OK: true}, nil
	}
}

// ── assign ───────────────────────────────────────────────────────────────────

// AssignIssueInput holds the parameters for replacing an issue's assignee set.
type AssignIssueInput struct {
	WorkspaceSlug string   `json:"workspace_slug"`
	Identifier    string   `json:"identifier"`
	AssigneeIDs   []string `json:"assignee_ids,omitempty"`
}

func assignIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[AssignIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AssignIssueInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		existing, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
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
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
	StateID       string `json:"state_id"`
}

func setIssueState(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[SetIssueStateInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetIssueStateInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		existing, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return nil, nil, err
		}
		sID, err := parseUUID(in.StateID, "state_id")
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
