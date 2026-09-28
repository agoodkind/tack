package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/service"
)

// searchUnavailableText is the exact response of every call while public
// search is disabled.
const searchUnavailableText = "Search is temporarily unavailable."

// SearchRunner runs one authenticated ranked search call.
type SearchRunner interface {
	Search(context.Context, service.SearchRequest) (service.SearchPage, error)
}

// SearchBinding connects tack_search to ranked search. While Runner or
// Cursors is nil, every call returns the fixed unavailable response.
type SearchBinding struct {
	Runner  SearchRunner
	Cursors *SearchCursorCodec
}

// RegisterSearch registers the one tack_search tool. While the binding has
// no runner or no cursor codec, the handler returns the fixed unavailable
// response before it reads any argument. Otherwise the same registration
// runs ranked search. The schema offers no scope arguments because ranked
// search covers the whole entry point.
func RegisterSearch(s *mcpserver.MCPServer, resolver *Resolver, binding SearchBinding) {
	tool := mcpmcp.Tool{
		Name: "tack_search",
		Description: "tack_search runs a ranked search over the nodes under one workspace. Each call returns at most 25 nodes. " +
			"Pass the returned cursor with the same workspace, query, and node_type to continue.",
		InputSchema: schema{
			Fields: append(entryPointSchemaFields(resolver),
				schemaField{Name: "query", Type: schemaString, Desc: "This argument is the search text.", Enum: nil},
				schemaField{Name: "node_type", Type: schemaString, Desc: "This optional argument limits results to one node type key or slug.", Enum: nil},
				schemaField{Name: searchCursorParam, Type: schemaString, Desc: "This optional argument accepts the cursor that the previous page returned.", Enum: nil},
			),
			Required: []string{resolver.EntryPointParamName(), "query"},
		}.toMCP(),
	}
	allowed := allowedArgNames(tool.InputSchema)
	handler := func(ctx context.Context, request mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		if binding.Runner == nil || binding.Cursors == nil {
			return searchUnavailable(), nil
		}
		if err := rejectUnknownArgs(request, tool.Name, allowed); err != nil {
			return recoverableError(err.Error()), nil
		}
		return runSearch(ctx, request, resolver, binding), nil
	}
	s.AddTool(tool, wrapToolHandler(tool.Name, handler))
}

func searchUnavailable() *mcpmcp.CallToolResult {
	return &mcpmcp.CallToolResult{
		IsError: true,
		Content: []mcpmcp.Content{mcpmcp.TextContent{Type: "text", Text: searchUnavailableText}},
	}
}
