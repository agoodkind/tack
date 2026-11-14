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

// argMap maps tool argument names to raw JSON values. Handlers decode
// individual arguments into concrete types instead of using any.
type argMap map[string]json.RawMessage

// bindArgs decodes the tool request arguments into argMap. Handlers read
// concrete values from this map instead of using any.
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

// requireString reads a required string argument from argMap. It returns
// true only for nonempty strings. Callers return recoverableError when
// requireString returns false.
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

// optionalString reads an optional string argument from argMap. It returns
// an empty string for missing arguments and values other than JSON strings.
// Filter fields use an empty string to omit the filter.
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

// parseProps decodes an object or JSON-encoded string into raw property values.
// Some MCP clients encode the inner JSON as a string (TACK-161, TACK-165).
// Empty input returns nil. Invalid input returns an error.
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

// registerTool wraps the handler with strict argument validation and
// wrapToolHandler, which records structured events and increments counters.
// Tool registration must use this function instead of s.AddTool.
// The service validates nested property keys through ValidateNodeProps.
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

// allowedArgNames returns the top-level argument keys that the tool's input
// schema declares. The argument wrapper uses this set after registration.
func allowedArgNames(s mcpmcp.ToolInputSchema) map[string]struct{} {
	allowed := make(map[string]struct{}, len(s.Properties))
	for name := range s.Properties {
		allowed[name] = struct{}{}
	}

	return allowed
}

// rejectUnknownArgs rejects top-level argument keys outside allowed.
// It accepts empty arguments and requests containing only declared keys.
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
