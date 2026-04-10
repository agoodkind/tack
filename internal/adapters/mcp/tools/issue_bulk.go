// Issue bulk operations: BulkUpdateIssues, BulkDeleteIssues, BulkMoveIssues.
package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ── bulk update ──────────────────────────────────────────────────────────────

// BulkUpdateIssuesInput holds the parameters for a bulk issue update.
type BulkUpdateIssuesInput struct {
	WorkspaceID string   `json:"workspace_id"`
	ProjectID   string   `json:"project_id"`
	IssueIDs    []string `json:"issue_ids"`
	StateID     *string  `json:"state_id,omitempty"`
	Priority    *string  `json:"priority,omitempty"`
	EpicID      *string  `json:"epic_id,omitempty"`
	ClearEpic   bool     `json:"clear_epic,omitempty"`
	AssigneeIDs []string `json:"assignee_ids,omitempty"`
}

// BulkUpdateIssuesOutput reports how many issues were updated.
type BulkUpdateIssuesOutput struct {
	Updated int `json:"updated"`
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
		issueIDs := make([]uuid.UUID, 0, len(in.IssueIDs))
		for _, s := range in.IssueIDs {
			id, parseErr := parseUUID(s, "issue_id")
			if parseErr != nil {
				return nil, BulkUpdateIssuesOutput{}, parseErr
			}
			issueIDs = append(issueIDs, id)
		}
		patch := issue.BulkUpdatePatch{
			IssueIDs:  issueIDs,
			ProjectID: pID,
			ActorID:   userID,
		}
		if in.StateID != nil {
			if id := parseOptionalUUID(*in.StateID); id != nil {
				patch.StateID = id
			}
		}
		if in.Priority != nil {
			p := issue.Priority(*in.Priority)
			patch.Priority = &p
		}
		if in.EpicID != nil || in.ClearEpic {
			patch.SetEpicID = true
			patch.EpicID = parseOptionalUUID(func() string {
				if in.EpicID != nil {
					return *in.EpicID
				}
				return ""
			}())
		}
		if len(in.AssigneeIDs) > 0 {
			ids := make([]uuid.UUID, 0, len(in.AssigneeIDs))
			for _, s := range in.AssigneeIDs {
				id, parseErr := parseUUID(s, "assignee_id")
				if parseErr != nil {
					return nil, BulkUpdateIssuesOutput{}, parseErr
				}
				ids = append(ids, id)
			}
			patch.AssigneeIDs = ids
		}
		updated, err := svc.BulkUpdate(ctx, wsID, patch)
		if err != nil {
			return nil, BulkUpdateIssuesOutput{}, err
		}
		return nil, BulkUpdateIssuesOutput{Updated: updated}, nil
	}
}

// ── bulk delete ──────────────────────────────────────────────────────────────

// BulkDeleteIssuesInput holds the parameters for a bulk issue delete.
type BulkDeleteIssuesInput struct {
	WorkspaceID string   `json:"workspace_id"`
	ProjectID   string   `json:"project_id"`
	IssueIDs    []string `json:"issue_ids"`
}

// BulkDeleteIssuesOutput reports how many issues were deleted.
type BulkDeleteIssuesOutput struct {
	Deleted int `json:"deleted"`
}

func bulkDeleteIssues(svc issue.Service) mcp.ToolHandlerFor[BulkDeleteIssuesInput, BulkDeleteIssuesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in BulkDeleteIssuesInput) (*mcp.CallToolResult, BulkDeleteIssuesOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, BulkDeleteIssuesOutput{}, err
		}
		issueIDs := make([]uuid.UUID, 0, len(in.IssueIDs))
		for _, s := range in.IssueIDs {
			id, parseErr := parseUUID(s, "issue_id")
			if parseErr != nil {
				return nil, BulkDeleteIssuesOutput{}, parseErr
			}
			issueIDs = append(issueIDs, id)
		}
		n, err := svc.BulkDelete(ctx, wsID, issueIDs)
		if err != nil {
			return nil, BulkDeleteIssuesOutput{}, err
		}
		return nil, BulkDeleteIssuesOutput{Deleted: n}, nil
	}
}

// ── bulk move ────────────────────────────────────────────────────────────────

// BulkMoveIssuesInput holds the parameters for a bulk issue move.
type BulkMoveIssuesInput struct {
	WorkspaceID     string   `json:"workspace_id"`
	ProjectID       string   `json:"project_id"`
	IssueIDs        []string `json:"issue_ids"`
	TargetProjectID string   `json:"target_project_id"`
}

// BulkMoveIssuesOutput reports how many issues were moved and how many failed.
type BulkMoveIssuesOutput struct {
	Moved  int `json:"moved"`
	Failed int `json:"failed"`
}

func bulkMoveIssues(svc issue.Service) mcp.ToolHandlerFor[BulkMoveIssuesInput, BulkMoveIssuesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in BulkMoveIssuesInput) (*mcp.CallToolResult, BulkMoveIssuesOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, BulkMoveIssuesOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, BulkMoveIssuesOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, BulkMoveIssuesOutput{}, err
		}
		targetPID, err := parseUUID(in.TargetProjectID, "target_project_id")
		if err != nil {
			return nil, BulkMoveIssuesOutput{}, err
		}
		issueIDs := make([]uuid.UUID, 0, len(in.IssueIDs))
		for _, s := range in.IssueIDs {
			id, parseErr := parseUUID(s, "issue_id")
			if parseErr != nil {
				return nil, BulkMoveIssuesOutput{}, parseErr
			}
			issueIDs = append(issueIDs, id)
		}
		moved, failed, err := svc.BulkMove(ctx, wsID, pID, issueIDs, targetPID, userID)
		if err != nil {
			return nil, BulkMoveIssuesOutput{}, err
		}
		return nil, BulkMoveIssuesOutput{Moved: moved, Failed: failed}, nil
	}
}
