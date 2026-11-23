package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
)

func registerWorkspaceList(s *mcpserver.MCPServer, reader node.NodeReader, resolver *Resolver) {
	registerTool(
		s,
		mcpmcp.Tool{
			Name:        "tack_list_workspaces",
			Description: "Lists workspaces the caller has access to.",
			InputSchema: schema{Fields: nil, Required: nil}.toMCP(),
		},
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			userID, err := mustUser(ctx)
			if err != nil {
				return unexpectedError(ctx, err), nil
			}

			wss, err := resolver.WorkspacesForUser(ctx, userID)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			rc := newRenderCtxWithTypes(ctx, reader, nil, resolver.typeIndex)

			return successText(renderList(rc, "workspaces", wss), ""), nil
		},
	)
}
