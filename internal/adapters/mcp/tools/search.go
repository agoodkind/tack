package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/issue"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterSearch(s *mcp.Server, svc issue.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across issues and epics in a workspace",
	}, search(svc))
}

type SearchInput struct {
	WorkspaceID string  `json:"workspace_id"`
	Query       string  `json:"query"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func search(svc issue.Service) mcp.ToolHandlerFor[SearchInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		filter := issue.ListFilter{}
		if in.ProjectID != nil {
			if id := parseOptionalUUID(*in.ProjectID); id != nil {
				filter.ProjectID = id
			}
		}
		items, total, err := svc.Search(ctx, wsID, in.Query, filter)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"items": items, "total": total}, nil
	}
}
