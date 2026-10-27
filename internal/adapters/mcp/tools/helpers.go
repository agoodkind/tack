package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/auth"
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
		wrapped := fmt.Errorf("bind arguments for tool %s: %w", req.Params.Name, err)
		slog.Error("mcp.arguments.bind_failed", slog.String("tool", req.Params.Name), slog.Any("err", wrapped))
		return nil, wrapped
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
//
// The wrapper enforces strict top-level argument keys before dispatch: any
// key not declared in tool.InputSchema.Properties causes a recoverableError.
// Nested keys inside properties are validated separately by the service
// layer (ValidateNodeProps).
func registerTool(s *mcpserver.MCPServer, tool mcpmcp.Tool, h mcpserver.ToolHandlerFunc) {
	allowed := allowedArgNames(tool.InputSchema)
	strict := func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		if err := rejectUnknownArgs(req, tool.Name, allowed); err != nil {
			return recoverableError(err.Error()), nil
		}
		return h(ctx, req)
	}
	s.AddTool(tool, wrapToolHandler(tool.Name, strict))
}

// allowedArgNames returns the set of top-level argument keys declared in
// the tool's input schema. The set is built once at registration time and
// captured by the strict-args wrapper.
func allowedArgNames(s mcpmcp.ToolInputSchema) map[string]struct{} {
	allowed := make(map[string]struct{}, len(s.Properties))
	for name := range s.Properties {
		allowed[name] = struct{}{}
	}
	return allowed
}

// rejectUnknownArgs returns an error if the request carries any top-level
// argument key not in allowed. Returns nil when the request has no
// arguments or every key is declared.
func rejectUnknownArgs(req mcpmcp.CallToolRequest, toolName string, allowed map[string]struct{}) error {
	var args map[string]json.RawMessage
	if err := req.BindArguments(&args); err != nil {
		wrapped := fmt.Errorf("bind arguments for tool %s: %w", toolName, err)
		slog.Error("mcp.arguments.bind_failed", slog.String("tool", toolName), slog.Any("err", wrapped))
		return wrapped
	}
	if len(args) == 0 {
		return nil
	}
	unknown := make([]string, 0)
	for name := range args {
		if _, ok := allowed[name]; ok {
			continue
		}
		unknown = append(unknown, name)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	allowedList := sortedKeyList(allowed)
	return fmt.Errorf(
		"unknown argument %s for tool %s; allowed: %s",
		quoteAndJoin(unknown),
		toolName,
		strings.Join(allowedList, ", "),
	)
}

func sortedKeyList(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteAndJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}
