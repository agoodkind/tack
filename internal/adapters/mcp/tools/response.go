package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
)

// success returns a successful tool result wrapping pre-formatted body
// text. Callers either build a typed payload and pass JSON bytes (legacy
// helpers below), or render markdown directly via renderNode/renderList
// and pass the result through successText.
//
// The closed type means no any reaches the response helper, even though it
// lives behind an unexported surface.
func success(body json.RawMessage, instruction string) *mcp.CallToolResult {
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	return successText(string(body), instruction)
}

// successText wraps a free-form text body in the success envelope. Use this
// when the tool's output is markdown (per the LLM-UX redesign) rather than
// JSON. The envelope is identical so MCP clients still parse it the same way.
func successText(body, instruction string) *mcp.CallToolResult {
	text := "<success>\n\n" + body
	if instruction != "" {
		text += "\n\n[LLM Instruction]: " + instruction
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// successJSON marshals a typed payload and forwards to success. Callers that
// already hold a typed value use this instead of marshalling at every site.
// T is constrained to any only because Go has no broader interface for
// "anything that can be JSON-marshaled"; the caller picks the concrete type.
func successJSON[T any](payload T, instruction string) *mcp.CallToolResult {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{}`)
	}
	return success(body, instruction)
}

// recoverableError returns an error result with a correction instruction the
// LLM can act on without yielding to the user (wrong param, not found,
// validation failure).
func recoverableError(instruction string) *mcp.CallToolResult {
	text := fmt.Sprintf("<error>\n\n[LLM Instruction]: %s", instruction)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// unexpectedError logs the real error and returns a sanitized result telling
// the LLM to yield to the user. Use for server errors, FDB failures, auth
// failures, timeouts.
func unexpectedError(ctx context.Context, err error) *mcp.CallToolResult {
	telemetry.L(ctx).Error("mcp tool: unexpected error", "err", err)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "<error>\n\n[LLM Instruction]: Unexpected error. Yield to user."}},
	}
}

// classifyError routes an error to recoverableError or unexpectedError based
// on domain type. Not found, invalid argument, already exists, failed
// precondition → recoverable with correction. Unauthenticated, permission
// denied, and all others → unexpected (yield to user).
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
