// Issue move operation: moves an issue to a different project with sequence reallocation.
package tools

import (
	"context"

	"goodkind.io/tack/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MoveIssueInput holds the parameters for moving an issue to a different project.
type MoveIssueInput struct {
	WorkspaceSlug           string `json:"workspace_slug"`
	Identifier              string `json:"identifier"`
	TargetProjectIdentifier string `json:"target_project_identifier"`
}

func moveIssue(svc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[MoveIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in MoveIssueInput) (*mcp.CallToolResult, any, error) {
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
		_, targetProj, err := r.Project(ctx, in.WorkspaceSlug, in.TargetProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		moved, err := svc.Move(ctx, ws.ID, proj.ID, existing.ID, targetProj.ID, userID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": moved}, nil
	}
}
