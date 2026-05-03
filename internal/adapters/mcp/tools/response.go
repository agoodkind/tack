package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
)

func successText(body, instruction string) *mcp.CallToolResult {
	text := body
	if instruction != "" {
		if text != "" && text[len(text)-1] != '\n' {
			text += "\n"
		}
		text += "\nNext step: " + instruction
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

func recoverableError(instruction string) *mcp.CallToolResult {
	text := fmt.Sprintf("#### Request could not be completed\n\n- Problem: %s\n\nNext step: Fix the request and retry once.", instruction)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}},
	}
}

func unexpectedError(ctx context.Context, err error) *mcp.CallToolResult {
	telemetry.L(ctx).Error("mcp.tool.unexpected_error", "err", err)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "#### Unexpected server error\n\nThe server could not complete the request safely.\n\nNext step: Yield to the user."}},
	}
}

func classifyError(ctx context.Context, err error) *mcp.CallToolResult {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return recoverableError(err.Error() +
			". Check tack_list_workspaces for workspace_slug, tack_describe_workspace for project_identifier, and list calls for node identifiers.")
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
