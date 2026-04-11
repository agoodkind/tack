package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func RegisterActivity(s *mcpserver.MCPServer, activity node.ActivityRepository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_get_activity",
		Description: "Gets the chronological activity log for any node (issue, epic, cycle, etc.) by its UUID node_id. Returns field-level change events with actor and timestamps. Obtain node_id from a prior tack_get_issue or tack_list_issues call.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}}, Required: []string{"workspace_slug", "node_id"}},
	}, getActivity(activity, r))
}

type GetActivityInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
}

func getActivity(activity node.ActivityRepository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetActivityInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		events, err := activity.List(ctx, ws.OrgID, ws.ID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"events": events, "total": len(events)}, ""), nil
	}
}
