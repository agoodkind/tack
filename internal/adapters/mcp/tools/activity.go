package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterActivity(s *mcp.Server, activity node.ActivityRepository, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_get_activity",
		Description: "Gets the chronological activity log for any node (issue, epic, cycle, etc.) by its UUID node_id. Returns field-level change events with actor and timestamps. Obtain node_id from a prior tack_get_issue or tack_list_issues call.",
	}, getActivity(activity, r))
}

type GetActivityInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	NodeID        string `json:"node_id"`
}

func getActivity(activity node.ActivityRepository, r *Resolver) mcp.ToolHandlerFor[GetActivityInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetActivityInput) (*mcp.CallToolResult, any, error) {
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		events, err := activity.List(ctx, ws.OrgID, ws.ID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"events": events, "total": len(events)}, ""), nil, nil
	}
}
