package tools

import (
	"context"

	domainsearch "goodkind.io/tack/internal/domain/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterSearch(s *mcp.Server, searcher domainsearch.Searcher) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across all node entities (issues, epics, projects, cycles, modules, workspaces) in a workspace",
	}, search(searcher))
}

type SearchInput struct {
	WorkspaceID string  `json:"workspace_id"`
	Query       string  `json:"query"`
	EntityType  *string `json:"entity_type,omitempty"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func search(searcher domainsearch.Searcher) mcp.ToolHandlerFor[SearchInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		_ = wsID
		filters := map[string]string{
			"workspace_id": in.WorkspaceID,
		}
		if in.EntityType != nil && *in.EntityType != "" {
			filters["entity_type"] = *in.EntityType
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			filters["project_id"] = *in.ProjectID
		}
		docs, err := searcher.Search(ctx, "nodes", in.Query, filters)
		if err != nil {
			return nil, nil, err
		}
		if docs == nil {
			docs = []domainsearch.NodeDoc{}
		}
		return nil, map[string]any{"items": docs, "total": len(docs)}, nil
	}
}
