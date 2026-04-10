package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterActivity(s *mcp.Server, activity node.ActivityRepository) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_get_activity",
		Description: "Get the chronological activity log for any node (issue, epic, cycle, etc.) using its node_id",
	}, getActivity(activity))
}

type GetActivityInput struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	NodeID      string `json:"node_id"`
}

func getActivity(activity node.ActivityRepository) mcp.ToolHandlerFor[GetActivityInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetActivityInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, nil, err
		}
		events, err := activity.List(ctx, orgID, wsID, nodeID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"events": events, "total": len(events)}, nil
	}
}
