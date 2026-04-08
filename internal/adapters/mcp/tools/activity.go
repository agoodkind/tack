package tools

import (
	"context"
	"encoding/json"

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
	OrgID       string `json:"org_id"       jsonschema:"required"`
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	NodeID      string `json:"node_id"      jsonschema:"required"`
}
type GetActivityOutput struct {
	Events json.RawMessage `json:"events"`
	Total  int             `json:"total"`
}

func getActivity(activity node.ActivityRepository) mcp.ToolHandlerFor[GetActivityInput, GetActivityOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetActivityInput) (*mcp.CallToolResult, GetActivityOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, GetActivityOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, GetActivityOutput{}, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, GetActivityOutput{}, err
		}
		events, err := activity.List(ctx, orgID, wsID, nodeID)
		if err != nil {
			return nil, GetActivityOutput{}, err
		}
		b, _ := json.Marshal(events)
		return nil, GetActivityOutput{Events: b, Total: len(events)}, nil
	}
}
