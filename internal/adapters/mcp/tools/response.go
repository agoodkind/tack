package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
)

// maxSuccessTextBytes limits the text that capText retains before adding
// the truncation notice. List pages reserve space for their cursor line.
// successText also applies capText to single-node output when an oversized
// property exceeds the limit. Error responses do not use capText.
const maxSuccessTextBytes = 32 * 1024

const truncationNotice = "\n\nOutput truncated at 32 KB. Narrow the request, lower `limit`, or use `cursor`."

func capText(text string) string {
	if len(text) <= maxSuccessTextBytes {
		return text
	}
	cut := strings.LastIndexByte(text[:maxSuccessTextBytes], '\n')
	if cut <= 0 {
		cut = maxSuccessTextBytes
	}
	return text[:cut] + truncationNotice
}

func successText(body, instruction string) *mcp.CallToolResult {
	text := body
	if instruction != "" {
		if text != "" && text[len(text)-1] != '\n' {
			text += "\n"
		}
		text += "\nNext step: " + instruction
	}
	text = capText(text)
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

const defaultRecoverableNextStep = "Fix the request and retry once."

func recoverableError(instruction string) *mcp.CallToolResult {
	return recoverableErrorWithNextStep(instruction, defaultRecoverableNextStep)
}

func recoverableErrorWithNextStep(problem, nextStep string) *mcp.CallToolResult {
	text := fmt.Sprintf("#### Request could not be completed\n\n- Problem: %s\n\nNext step: %s", problem, nextStep)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

// permissionDenied returns the error result that the tool wrapper uses
// when a successful handler has not passed a membership check.
func permissionDenied() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "#### Permission denied\n\nThe caller is not a member of the org that holds this data.\n\nNext step: Yield to the user."}},
	}
}

func unexpectedError(ctx context.Context, err error) *mcp.CallToolResult {
	telemetry.L(ctx).Error("mcp.tool.unexpected_error", "err", err)
	noteOutcome(ctx, audit.OutcomeFailed, "unexpected_error", "the server could not complete the tool call")
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "#### Unexpected server error\n\nThe server could not complete the request safely.\n\nNext step: Yield to the user."}},
	}
}

// classifyError converts err to a tool response and notes its typed refusal
// code in the request context for audit recording.
func classifyError(ctx context.Context, err error) *mcp.CallToolResult {
	noteRefusal(ctx, err)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return recoverableError(err.Error() +
			". Check tack_list_workspaces for workspace_reference, tack_describe_workspace for scope references, and list calls for node references.")
	case errors.Is(err, domain.ErrInvalidArgument):
		return recoverableError(err.Error() + ". Fix the parameter value and retry.")
	case errors.Is(err, domain.ErrAlreadyExists):
		return recoverableError(err.Error() + ". Use a different name or update the existing entity.")
	case errors.Is(err, domain.ErrConflict):
		return recoverableError(err.Error() + ". Re-read the current entity before retrying.")
	case errors.Is(err, domain.ErrFailedPrecondition):
		return recoverableError(err.Error() + ". Check the current state before retrying.")
	default:
		return unexpectedError(ctx, err)
	}
}
