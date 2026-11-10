package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterPrompts registers the orientation prompt.
//
// The prompt supports clients that use prompts rather than resources.
// Its registration provides:
//
//   - The prompt uses the name tack_getting_started.
//   - Its content matches the tack://getting-started resource.
func RegisterPrompts(
	s *mcpserver.MCPServer,
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) {
	s.AddPrompt(
		mcpmcp.Prompt{
			Name:        "tack_getting_started",
			Description: "Orientation guide for Tack MCP. Read once per session before calling any other Tack tool.",
		},
		func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			text := buildGettingStartedText(resolver, nodeTypes, propertyDefs)

			return &mcpmcp.GetPromptResult{
				Description: "Tack orientation",
				Messages: []mcpmcp.PromptMessage{
					{
						Role:    mcpmcp.RoleAssistant,
						Content: mcpmcp.TextContent{Type: "text", Text: text},
					},
				},
			}, nil
		},
	)
}
