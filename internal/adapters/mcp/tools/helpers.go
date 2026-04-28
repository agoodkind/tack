package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/telemetry"
)

// argMap is the typed shape every Tack tool handler reads its arguments
// through. mcp-go decodes the request arguments into a generic map of raw
// JSON values so each handler can pull out exactly the fields it needs with
// concrete types instead of going through any.
type argMap map[string]json.RawMessage

// bindArgs decodes the tool request arguments into argMap. The conversion
// from mcp-go's untyped request body to our typed map happens here, and
// only here, so handler code never touches any.
func bindArgs(req mcpmcp.CallToolRequest) (argMap, error) {
	var m argMap
	if err := req.BindArguments(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// requireString reads a required string argument from argMap. The returned
// ok==true only when the value is a non-empty string. Any other shape
// (missing, wrong type, empty string) returns ok==false so the caller can
// short-circuit with recoverableError. Centralised so tools cannot silently
// fall through with an empty string after a failed assertion.
func requireString(args argMap, name string) (string, bool) {
	raw, ok := args[name]
	if !ok || len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	if s == "" {
		return "", false
	}
	return s, true
}

// optionalString reads an optional string argument from argMap. Returns ""
// when the key is absent or the value is not a JSON string. Used for filter
// fields where empty means "no filter".
func optionalString(args argMap, name string) string {
	raw, ok := args[name]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// parseProps coerces a properties payload into map[string]json.RawMessage.
// Tolerates two shapes the MCP transport produces.
//
// Shape 1: object. The right wire form. raw decodes directly into the
// returned map.
//
// Shape 2: string. Some clients (notably Claude Code via certain SDK
// paths) stringify the inner JSON before sending. Without this branch, the
// server silently dropped every property in tack_create_*. See TACK-161
// and TACK-165.
//
// Returns nil for nil or empty input. Returns an error only when raw is a
// string that is not valid JSON. An unparseable shape is the caller's bug
// to fix at the boundary, not something to swallow.
func parseProps(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err == nil {
		return m, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return parseProps(json.RawMessage(s))
	}
	return nil, errors.New("properties must be a JSON object like {\"key\": \"value\"} or a JSON-encoded string of one")
}

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
