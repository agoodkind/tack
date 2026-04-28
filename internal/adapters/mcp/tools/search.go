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
			InputSchema: schema{
				Fields: []schemaField{
					{Name: resolver.EntryPointParamName(), Type: schemaString},
					{Name: "query", Type: schemaString},
					{Name: "node_type", Type: schemaString},
				},
				Required: []string{resolver.EntryPointParamName(), "query"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return recoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			query, ok := requireString(args, "query")
			if !ok {
				return recoverableError("query is required"), nil
			}
			nodeTypeFilter := optionalString(args, "node_type")

			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			filters := map[string]string{"org_id": ws.OrgID.String()}
			if nodeTypeFilter != "" {
				filters["node_type"] = nodeTypeFilter
			}
			docs, _, err := searcher.Search(ctx, "nodes", query, filters)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			hits := make([]searchHit, 0, len(docs))
			for _, d := range docs {
				hits = append(hits, searchHit{ID: d.ID, NodeType: d.NodeType, Name: d.Name})
			}
			return successText(renderSearchHits(hits), ""), nil
		},
	)
}
