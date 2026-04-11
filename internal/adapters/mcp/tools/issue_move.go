// Issue move operation: moves an issue to a different project with sequence reallocation.
package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/service"
)

// MoveIssueInput holds the parameters for moving an issue to a different project.
type MoveIssueInput struct {
	WorkspaceSlug           string `json:"workspace_slug"`
	Identifier              string `json:"identifier"`
	TargetProjectIdentifier string `json:"target_project_identifier"`
}

func moveIssue(svc *service.IssueService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in MoveIssueInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		existing, err := svc.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		_, targetProj, err := r.Project(ctx, in.WorkspaceSlug, in.TargetProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		moved, err := svc.Move(ctx, ws.ID, proj.ID, existing.ID, targetProj.ID, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"issue": moved}, "Issue moved. The identifier has changed to reflect the new project."), nil
	}
}
