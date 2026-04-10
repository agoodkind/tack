// Issue move operation: moves an issue to a different project with sequence reallocation.
package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/issue"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MoveIssueInput holds the parameters for moving an issue to a different project.
type MoveIssueInput struct {
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	IssueID         string `json:"issue_id"`
	TargetProjectID string `json:"target_project_id"`
}

func moveIssue(svc issue.Service) mcp.ToolHandlerFor[MoveIssueInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in MoveIssueInput) (*mcp.CallToolResult, any, error) {
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
		targetPID, err := parseUUID(in.TargetProjectID, "target_project_id")
		if err != nil {
			return nil, nil, err
		}
		moved, err := svc.Move(ctx, wsID, pID, iID, targetPID, userID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"issue": moved}, nil
	}
}
