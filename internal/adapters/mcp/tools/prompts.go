package tools

import (
	"context"
	"fmt"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterPrompts registers a single orientation prompt. Detailed per-type
// prompts are deferred until we confirm what the LLM actually needs.
func RegisterPrompts(s *mcpserver.MCPServer, nodeTypes []*node.NodeType) {
	s.AddPrompt(
		mcpmcp.Prompt{
			Name:        "tack_onboard",
			Description: "Orientation prompt: call tack_list_workspaces first, then tack_describe_<workspace>.",
		},
		func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			msg := fmt.Sprintf("You have %d node types available. Start by calling tack_list_workspaces.", len(nodeTypes))
			return &mcpmcp.GetPromptResult{
				Description: "Tack orientation",
				Messages: []mcpmcp.PromptMessage{
					{Role: mcpmcp.RoleAssistant, Content: mcpmcp.TextContent{Type: "text", Text: msg}},
				},
			}, nil
		},
	)
}
