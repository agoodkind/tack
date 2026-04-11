package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Success returns a successful tool result. data is JSON-marshaled; instruction
// is optional next-step guidance for the LLM. Pass empty string for no instruction.
func Success(data any, instruction string) *mcp.CallToolResult {
	body, err := json.Marshal(data)
	if err != nil {
		body = []byte(`{}`)
	}
	text := "<success>\n\n" + string(body)
	if instruction != "" {
		text += "\n\n[LLM Instruction]: " + instruction
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// RecoverableError returns an error result with a correction instruction the LLM
// can act on without yielding to the user (wrong param, not found, validation failure).
func RecoverableError(instruction string) *mcp.CallToolResult {
	text := fmt.Sprintf("<error>\n\n[LLM Instruction]: %s", instruction)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// UnexpectedError logs the real error and returns a sanitized result telling the LLM
// to yield to the user. Use for server errors, FDB failures, auth failures, timeouts.
func UnexpectedError(ctx context.Context, err error) *mcp.CallToolResult {
	telemetry.L(ctx).Error("mcp tool: unexpected error", "err", err)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "<error>\n\n[LLM Instruction]: Unexpected error. Yield to user."}},
	}
}
