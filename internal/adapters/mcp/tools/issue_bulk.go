// Issue bulk operations: BulkUpdateIssues, BulkDeleteIssues, BulkMoveIssues.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/service"
)

// ── bulk update ──────────────────────────────────────────────────────────────

// BulkUpdateIssuesInput holds the parameters for a bulk issue update.
type BulkUpdateIssuesInput struct {
	WorkspaceSlug string   `json:"workspace_slug"`
	Identifiers   []string `json:"identifiers"`
	StateID       *string  `json:"state_id,omitempty"`
	Priority      *string  `json:"priority,omitempty"`
	EpicID        *string  `json:"epic_id,omitempty"`
	ClearEpic     bool     `json:"clear_epic,omitempty"`
	AssigneeIDs   []string `json:"assignee_ids,omitempty"`
}

// BulkUpdateIssuesOutput reports how many issues were updated.
type BulkUpdateIssuesOutput struct {
	Updated int `json:"updated"`
}

func bulkUpdateIssues(svc *service.IssueService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in BulkUpdateIssuesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		issueIDs := make([]uuid.UUID, 0, len(in.Identifiers))
		var ws *workspace.Workspace
		var proj *project.Project
		for _, ident := range in.Identifiers {
			projIdent, seq, err := ParseNodeIdentifier(ident)
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			if ws == nil {
				ws, proj, err = r.Project(ctx, in.WorkspaceSlug, projIdent)
				if err != nil {
					return ClassifyError(ctx, err), nil
				}
			} else if !strings.EqualFold(proj.Identifier, projIdent) {
				return RecoverableError(fmt.Sprintf("bulk update requires all issues in the same project: got %q and %q", proj.Identifier, projIdent)), nil
			}
			issueObj, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			issueIDs = append(issueIDs, issueObj.ID)
		}
		patch := issue.BulkUpdatePatch{
			IssueIDs:  issueIDs,
			ProjectID: proj.ID,
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
					return RecoverableError(parseErr.Error()), nil
				}
				ids = append(ids, id)
			}
			patch.AssigneeIDs = ids
		}
		updated, err := svc.BulkUpdate(ctx, ws.ID, patch)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(BulkUpdateIssuesOutput{Updated: updated}, ""), nil
	}
}

// ── bulk delete ──────────────────────────────────────────────────────────────

// BulkDeleteIssuesInput holds the parameters for a bulk issue delete.
type BulkDeleteIssuesInput struct {
	WorkspaceSlug string   `json:"workspace_slug"`
	Identifiers   []string `json:"identifiers"`
}

// BulkDeleteIssuesOutput reports how many issues were deleted.
type BulkDeleteIssuesOutput struct {
	Deleted int `json:"deleted"`
}

func bulkDeleteIssues(svc *service.IssueService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in BulkDeleteIssuesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		issueIDs := make([]uuid.UUID, 0, len(in.Identifiers))
		for _, ident := range in.Identifiers {
			projIdent, seq, err := ParseNodeIdentifier(ident)
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			_, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			issueObj, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			issueIDs = append(issueIDs, issueObj.ID)
		}
		n, err := svc.BulkDelete(ctx, ws.ID, issueIDs)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(BulkDeleteIssuesOutput{Deleted: n}, "Deletion is permanent and irreversible."), nil
	}
}

// ── bulk move ────────────────────────────────────────────────────────────────

// BulkMoveIssuesInput holds the parameters for a bulk issue move.
type BulkMoveIssuesInput struct {
	WorkspaceSlug           string   `json:"workspace_slug"`
	Identifiers             []string `json:"identifiers"`
	TargetProjectIdentifier string   `json:"target_project_identifier"`
}

// BulkMoveIssuesOutput reports how many issues were moved and how many failed.
type BulkMoveIssuesOutput struct {
	Moved  int `json:"moved"`
	Failed int `json:"failed"`
}

func bulkMoveIssues(svc *service.IssueService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in BulkMoveIssuesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		issueIDs := make([]uuid.UUID, 0, len(in.Identifiers))
		var ws *workspace.Workspace
		var proj *project.Project
		for _, ident := range in.Identifiers {
			projIdent, seq, err := ParseNodeIdentifier(ident)
			if err != nil {
				return RecoverableError(err.Error()), nil
			}
			if ws == nil {
				ws, proj, err = r.Project(ctx, in.WorkspaceSlug, projIdent)
				if err != nil {
					return ClassifyError(ctx, err), nil
				}
			} else if !strings.EqualFold(proj.Identifier, projIdent) {
				return RecoverableError(fmt.Sprintf("bulk move requires all issues in the same project: got %q and %q", proj.Identifier, projIdent)), nil
			}
			issueObj, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			issueIDs = append(issueIDs, issueObj.ID)
		}
		_, targetProj, err := r.Project(ctx, in.WorkspaceSlug, in.TargetProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		moved, failed, err := svc.BulkMove(ctx, ws.ID, proj.ID, issueIDs, targetProj.ID, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(BulkMoveIssuesOutput{Moved: moved, Failed: failed}, ""), nil
	}
}
