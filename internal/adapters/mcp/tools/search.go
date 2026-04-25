package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// RegisterSearch registers tack_search.
func RegisterSearch(s *mcpserver.MCPServer, searcher domainsearch.Searcher, resolver *Resolver) {
	registerTool(s, 
		mcpmcp.Tool{
			Name:        "tack_search",
			Description: "Full-text search across all nodes in a workspace's org. Filters are raw (field=value) equality.",
			InputSchema: mcpmcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					resolver.EntryPointParamName(): map[string]any{"type": "string"},
					"query":                         map[string]any{"type": "string"},
					"node_type":                     map[string]any{"type": "string"},
				},
				Required: []string{resolver.EntryPointParamName(), "query"},
			},
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args := req.GetArguments()
			slug, _ := args[resolver.EntryPointParamName()].(string)
			query, _ := args["query"].(string)
			nodeTypeFilter, _ := args["node_type"].(string)

			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			filters := map[string]string{"org_id": ws.OrgID.String()}
			if nodeTypeFilter != "" {
				filters["node_type"] = nodeTypeFilter
			}
			docs, facets, err := searcher.Search(ctx, "nodes", query, filters)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			return Success(map[string]any{"results": docs, "facets": facets}, ""), nil
		},
	)
}
