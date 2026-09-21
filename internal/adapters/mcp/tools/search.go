package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterSearch bypasses argument binding so every request receives the fixed
// outage response.
func RegisterSearch(s *mcpserver.MCPServer, resolver *Resolver) {
	tool := mcpmcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across all nodes in a workspace's org. Filters are raw (field=value) equality.",
		InputSchema: schema{
			Fields: append(append(entryPointSchemaFields(resolver),
				schemaField{Name: "query", Type: schemaString},
				schemaField{Name: "node_type", Type: schemaString},
			), searchScopeFields(resolver)...),
			Required: []string{resolver.EntryPointParamName(), "query"},
		}.toMCP(),
	}
	handler := func(_ context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		return &mcpmcp.CallToolResult{
			IsError: true,
			Content: []mcpmcp.Content{
				mcpmcp.TextContent{Type: "text", Text: "Search is temporarily unavailable."},
			},
		}, nil
	}
	s.AddTool(tool, wrapToolHandler(tool.Name, handler))
}

func searchScopeFields(resolver *Resolver) []schemaField {
	fields := make([]schemaField, 0, len(resolver.scopeChain))
	for _, level := range resolver.scopeChain {
		fields = append(fields, scopeReferenceFields(level, resolver)...)
	}
	return fields
}
