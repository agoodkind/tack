package tools

import (
	"context"

	"github.com/agoodkind/tack/internal/domain/issue"
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
type SearchOutput struct {
	Items any `json:"items"`
	Total int             `json:"total"`
}

func search(svc issue.Service) mcp.ToolHandlerFor[SearchInput, SearchOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, SearchOutput{}, err
		}
		filter := issue.ListFilter{}
		if in.ProjectID != nil {
			if id := parseOptionalUUID(*in.ProjectID); id != nil {
				filter.ProjectID = id
			}
		}
		items, total, err := svc.Search(ctx, wsID, in.Query, filter)
		if err != nil {
			return nil, SearchOutput{}, err
		}
		return nil, SearchOutput{Items: items, Total: total}, nil
	}
}
