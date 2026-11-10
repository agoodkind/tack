package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/service"
)

// searchUnavailableText supplies the fixed error text when the search
// binding lacks a runner or cursor codec, or the backend is unavailable.
const searchUnavailableText = "Search is temporarily unavailable."

// SearchRunner runs one authenticated ranked search call.
type SearchRunner interface {
	Search(context.Context, service.SearchRequest) (service.SearchPage, error)
}

// SearchBinding supplies ranked search and cursor authentication to
// tack_search. A nil Runner or Cursors returns the fixed unavailable
// response.
type SearchBinding struct {
	Runner  SearchRunner
	Cursors *SearchCursorCodec
}

// RegisterSearch registers tack_search for ranked search in one entry point.
//
// A missing runner or cursor codec returns the fixed unavailable response
// before argument handling. The schema omits lower scope arguments because
// ranked search covers the whole entry point.
//
// The registered tool accepts these inputs:
//
//   - The entry point reference selects the workspace.
//   - query supplies search text.
//   - node_type optionally limits the type.
//   - cursor optionally continues a search.
func RegisterSearch(s *mcpserver.MCPServer, nodes NodeTypeBinding, binding SearchBinding) {
	resolver := nodes.Resolver
	tool := mcpmcp.Tool{
		Name: "tack_search",
		Description: "tack_search runs a ranked search over the nodes under one workspace. Each call returns at most 25 nodes. " +
			"Pass the returned cursor with the same workspace, query, and node_type to continue.",
		InputSchema: schema{
			Fields: append(
				entryPointSchemaFields(resolver),
				schemaField{
					Name: "query",
					Type: schemaString,
					Desc: "This argument is the search text.",
					Enum: nil,
				},
				schemaField{
					Name: "node_type",
					Type: schemaString,
					Desc: "This optional argument limits results to one node type key or slug.",
					Enum: nil,
				},
				schemaField{
					Name: searchCursorParam,
					Type: schemaString,
					Desc: "This optional argument accepts the cursor that the previous page returned.",
					Enum: nil,
				},
			),
			Required: []string{resolver.EntryPointParamName(), "query"},
		}.toMCP(),
	}
	allowed := allowedArgNames(tool.InputSchema)
	handler := func(ctx context.Context, request mcpmcp.CallToolRequest) (
		*mcpmcp.CallToolResult,
		error,
	) {
		if binding.Runner == nil || binding.Cursors == nil {
			return searchUnavailable(ctx), nil
		}

		// A caller without an organization lacks an entry point type, and its
		// schema uses "_reference" for the entry argument. Refuse the call
		// before argument validation.
		if !resolver.HasEntryPoint() {
			return permissionDenied(), nil
		}

		if err := rejectUnknownArgs(request, tool.Name, allowed); err != nil {
			return recoverableError(err.Error()), nil
		}

		return runSearch(ctx, request, nodes, binding), nil
	}
	s.AddTool(tool, wrapToolHandler(tool.Name, handler))
}

// searchUnavailable notes the search_unavailable failure in the request
// context for audit recording and returns the fixed unavailable response.
func searchUnavailable(ctx context.Context) *mcpmcp.CallToolResult {
	noteOutcome(
		ctx,
		audit.OutcomeFailed,
		"search_unavailable",
		"the search backend could not answer the request",
	)

	return searchUnavailableResult()
}

// searchRecordingFailed notes a failed read outcome in the request context
// with code audit_recording_failed and returns the unavailable response.
func searchRecordingFailed(ctx context.Context) *mcpmcp.CallToolResult {
	noteOutcome(
		ctx,
		audit.OutcomeFailed,
		"audit_recording_failed",
		"the search could not save required audit evidence",
	)

	return searchUnavailableResult()
}

func searchUnavailableResult() *mcpmcp.CallToolResult {
	return &mcpmcp.CallToolResult{
		IsError: true,
		Content: []mcpmcp.Content{mcpmcp.TextContent{Type: "text", Text: searchUnavailableText}},
	}
}

// HasEntryPoint reports whether the resolver has an entry point slug.
//
// NewResolver sets the slug from a type with FeatureIsEntryPoint. A caller
// without an organization does not have an entry point type.
//
// The search handler refuses a caller without an entry point:
//
//	if !resolver.HasEntryPoint() {
//		return permissionDenied(), nil
//	}
func (r *Resolver) HasEntryPoint() bool {
	return r.entryPointSlug != ""
}
