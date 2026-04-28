package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
	"github.com/mark3labs/mcp-go/mcp"
)

// Success returns a successful tool result. data is JSON-marshaled; instruction
// is optional next-step guidance for the LLM. Pass empty string for no instruction.
func success(data any, instruction string) *mcp.CallToolResult {
	body, err := json.Marshal(data)
	if err != nil {
		body = []byte(`{}`)
	}
	text := "<success>\n\n" + string(body)
	if instruction != "" {
		text += "\n\n[LLM Instruction]: " + instruction
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// RecoverableError returns an error result with a correction instruction the LLM
// can act on without yielding to the user (wrong param, not found, validation failure).
func recoverableError(instruction string) *mcp.CallToolResult {
	text := fmt.Sprintf("<error>\n\n[LLM Instruction]: %s", instruction)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// UnexpectedError logs the real error and returns a sanitized result telling the LLM
// to yield to the user. Use for server errors, FDB failures, auth failures, timeouts.
func unexpectedError(ctx context.Context, err error) *mcp.CallToolResult {
	telemetry.L(ctx).Error("mcp tool: unexpected error", "err", err)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "<error>\n\n[LLM Instruction]: Unexpected error. Yield to user."}},
	}
}

// ClassifyError routes an error to RecoverableError or UnexpectedError based on domain type.
// Not found, invalid argument, already exists, failed precondition → recoverable with correction.
// Unauthenticated, permission denied, and all others → unexpected (yield to user).
func classifyError(ctx context.Context, err error) *mcp.CallToolResult {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return recoverableError(err.Error() +
			". Check: (1) call tack_list_workspaces to verify workspace_slug, " +
			"(2) call tack_describe_workspace to verify project_identifier, " +
			"(3) for node_id, use the UUID or identifier (e.g. TACK-65) from a list call.")
	case errors.Is(err, domain.ErrInvalidArgument):
		return recoverableError(err.Error() + ". Fix the parameter value and retry.")
	case errors.Is(err, domain.ErrAlreadyExists):
		return recoverableError(err.Error() + ". An entity with this identifier already exists. Use a different name or update the existing one.")
	case errors.Is(err, domain.ErrFailedPrecondition):
		return recoverableError(err.Error() + ". A required condition was not met. Check the current state before retrying.")
	default:
		return unexpectedError(ctx, err)
	}
}
