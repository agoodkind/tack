package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterPrompts registers the orientation prompt. It mirrors the
// tack://getting-started resource so callers who prefer prompts over
// resources get the same content.
func RegisterPrompts(s *mcpserver.MCPServer, resolver *Resolver, nodeTypes []*node.NodeType) {
	s.AddPrompt(
		mcpmcp.Prompt{
			Name:        "tack_getting_started",
			Description: "Orientation guide for Tack MCP. Read once per session before calling any other Tack tool.",
		},
		func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			text := buildGettingStartedText(resolver, nodeTypes)
			return &mcpmcp.GetPromptResult{
				Description: "Tack orientation",
				Messages: []mcpmcp.PromptMessage{
					{Role: mcpmcp.RoleAssistant, Content: mcpmcp.TextContent{Type: "text", Text: text}},
				},
			}, nil
		},
	)
}
