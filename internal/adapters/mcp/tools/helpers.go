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

// argMap stores tool arguments as raw JSON values. Handlers decode the
// fields they need into concrete types without using any.
type argMap map[string]json.RawMessage

// bindArgs binds tool request arguments to argMap.
func bindArgs(req mcpmcp.CallToolRequest) (argMap, error) {
	var m argMap
	if err := req.BindArguments(&m); err != nil {
		wrapped := fmt.Errorf("bind arguments for tool %s: %w", req.Params.Name, err)
		slog.Error(
			"mcp.arguments.bind_failed",
			slog.String("tool", req.Params.Name),
			slog.Any("err", wrapped),
		)

		return nil, wrapped
	}

	return m, nil
}

// requireString returns a string and true for a nonempty JSON string in
// argMap. It returns an empty string and false in these cases:
//
//   - argMap omits the key.
//   - The input has zero bytes.
//   - The input is not a JSON string.
//   - The input decodes to an empty string.
//
// Handlers report a failed required string check with recoverableError
// before using the required value. Intermediate helpers may return the
// check result or a missing argument message to their callers. Handlers
// return the error result with a nil Go error.
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

// optionalString returns a string from argMap or an empty string when argMap
// omits the key or the value is not a JSON string. Filter fields use an
// empty string to omit a filter.
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

// parseProps decodes properties into map[string]json.RawMessage.
//
// It returns nil for nil or empty input, JSON null, or an empty encoded
// string. It returns an error for invalid JSON or an unsupported shape.
// The caller must handle that error at the input boundary.
//
// parseProps accepts these forms:
//
//   - A JSON object decodes directly into the property map.
//   - A JSON string decodes its contents recursively as another properties
//     payload.
//
// TACK-161 added support for clients, including Claude Code through certain
// SDK paths, that encoded inner JSON as a string. Earlier tack_create_*
// handlers silently omitted the supplied properties for that form. TACK-165
// preserved encoded-string compatibility and added recursive decoding for
// double-encoded strings.
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

	return nil, errors.New(
		"properties must be a JSON object like {\"key\": \"value\"} or a JSON-encoded string of one",
	)
}

func mustUser(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, errors.New("unauthenticated")
	}

	return id, nil
}

// registerTool registers an MCP tool through wrapToolHandler. Use
// registerTool instead of direct AddTool calls in this package. The wrapper
// logs tool calls and updates counters after checking required recorders. A
// missing required recorder refuses the call before those logging and
// counter operations.
//
// The registration wrapper rejects top-level keys absent from
// tool.InputSchema.Properties with recoverableError. The service validates
// nested property keys through ValidateNodeProps.
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

// allowedArgNames returns the top-level keys from the tool's input schema.
// Registration computes the set once, and the argument wrapper reuses it.
func allowedArgNames(s mcpmcp.ToolInputSchema) map[string]struct{} {
	allowed := make(map[string]struct{}, len(s.Properties))
	for name := range s.Properties {
		allowed[name] = struct{}{}
	}

	return allowed
}

// rejectUnknownArgs returns an error for top-level argument keys outside
// allowed. It returns nil when arguments are absent or every key belongs to
// allowed.
func rejectUnknownArgs(
	req mcpmcp.CallToolRequest,
	toolName string,
	allowed map[string]struct{},
) error {
	var args map[string]json.RawMessage
	if err := req.BindArguments(&args); err != nil {
		wrapped := fmt.Errorf("bind arguments for tool %s: %w", toolName, err)
		slog.Error(
			"mcp.arguments.bind_failed",
			slog.String("tool", toolName),
			slog.Any("err", wrapped),
		)

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
