package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterResources registers MCP resources. Content is generated from the
// current metadata set so that custom types surface automatically without
// editing string literals.
func RegisterResources(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) {
	s.AddResource(
		mcpmcp.Resource{
			URI:         "tack://getting-started",
			Name:        "tack-getting-started",
			Description: "Orientation guide. Read once per session before calling any other Tack tool.",
			MIMEType:    "text/markdown",
		},
		func(_ context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{URI: "tack://getting-started", MIMEType: "text/markdown", Text: buildGettingStartedText(resolver, nodeTypes, propertyDefs)},
			}, nil
		},
	)
}
