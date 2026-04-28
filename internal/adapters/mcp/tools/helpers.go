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

// parseProps coerces a properties payload into map[string]json.RawMessage.
// It tolerates the three shapes the MCP transport produces.
//
// Shape 1 is map[string]any. That is the normal object case after JSON
// unmarshalling.
//
// Shape 2 is string. Some clients (notably Claude Code via certain SDK
// paths) stringify the inner JSON before sending. Without this branch, the
// server silently dropped every property in tack_create_*. See TACK-161.
//
// Shape 3 is json.RawMessage. That is what callers see when binding through
// a typed struct.
//
// Returns nil for nil or empty input. Returns an error only when v is a
// string that is not valid JSON. An unparseable shape is the caller's bug
// to fix at the boundary, not something to swallow.
func parseProps(v any) (map[string]json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]json.RawMessage, len(val))
		for k, vv := range val {
			raw, err := json.Marshal(vv)
			if err != nil {
				return nil, err
			}
			out[k] = raw
		}
		return out, nil
	case string:
		if val == "" {
			return nil, nil
		}
		var inner map[string]any
		if err := json.Unmarshal([]byte(val), &inner); err != nil {
			return nil, err
		}
		return parseProps(inner)
	case json.RawMessage:
		if len(val) == 0 {
			return nil, nil
		}
		// Try the object shape first. That is the right wire form.
		var inner map[string]any
		if err := json.Unmarshal(val, &inner); err == nil {
			return parseProps(inner)
		}
		// Fall back: some LLM transports stringify the properties value, so
		// what arrives is a JSON string whose contents are themselves JSON.
		// Decode the outer string layer and recurse. See TACK-165.
		var s string
		if err := json.Unmarshal(val, &s); err == nil {
			return parseProps(s)
		}
		return nil, errors.New("properties must be a JSON object like {\"key\": \"value\"} or a JSON-encoded string of one")
	default:
		return nil, errors.New("properties must be an object or a JSON-encoded string")
	}
}

// requireString reads a required string argument from the MCP args map. The
// returned error is nil and ok==true only when the value is a non-empty
// string. Any other shape (missing, wrong type, empty string) returns ok==
// false so the caller can short-circuit with RecoverableError. Centralised
// so tools cannot silently fall through with an empty string after a typed
// assertion miss.
func requireString(args map[string]any, name string) (string, bool) {
	v, ok := args[name].(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
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
