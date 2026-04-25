package tools

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/telemetry"
)

func mustUser(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, errors.New("unauthenticated")
	}
	return id, nil
}

// registerTool registers an MCP tool with the server, wrapping its handler
// in wrapToolHandler so every call lands one structured pair of events plus
// counter bumps. Use this in place of s.AddTool everywhere in the
// internal/adapters/mcp/tools package.
func registerTool(s *mcpserver.MCPServer, tool mcpmcp.Tool, h mcpserver.ToolHandlerFunc) {
	s.AddTool(tool, wrapToolHandler(tool.Name, h))
}

// wrapToolHandler instruments an MCP tool handler with structured logging
// and expvar counters. Every tool call emits exactly two events:
//
//   - mcp.tool.started (Debug) on entry with tool name
//   - mcp.tool.completed (Info, status=ok) or mcp.tool.failed (Warn,
//     status=failed/error_response) on exit, with duration_ms
//
// The "error_response" status is used when the inner handler returned a
// CallToolResult with IsError=true. Those results are still nil-error
// from Go's perspective; they signal that the LLM should self-correct.
//
// IncMCPTool and IncMCPToolErr update process-global expvars for /debug/vars.
//
// Wrap once per tool registration; the closure captures the tool name so
// callers do not need to thread it through.
func wrapToolHandler(name string, h mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		start := time.Now()
		log := telemetry.L(ctx)
		log.DebugContext(ctx, "mcp.tool.started", slog.String("tool", name))

		res, err := h(ctx, req)
		dur := time.Since(start)
		telemetry.IncMCPTool(name)

		switch {
		case err != nil:
			telemetry.IncMCPToolErr(name)
			log.WarnContext(ctx, "mcp.tool.failed",
				slog.String("tool", name),
				slog.String("status", "failed"),
				slog.Int64("duration_ms", dur.Milliseconds()),
				slog.String("err", err.Error()),
			)
		case res != nil && res.IsError:
			telemetry.IncMCPToolErr(name)
			log.WarnContext(ctx, "mcp.tool.failed",
				slog.String("tool", name),
				slog.String("status", "error_response"),
				slog.Int64("duration_ms", dur.Milliseconds()),
			)
		default:
			log.InfoContext(ctx, "mcp.tool.completed",
				slog.String("tool", name),
				slog.String("status", "ok"),
				slog.Int64("duration_ms", dur.Milliseconds()),
			)
		}
		return res, err
	}
}
