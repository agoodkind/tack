package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// auditRecorder is the audit sink used by every wrapped tool handler. It
// is set once at startup via SetAuditRecorder. Tools that registered before
// the setter ran (none in current code) would silently noop; we use atomic
// load to keep that path branch-free.
var auditRecorder atomic.Value // holds *auditRecorderValue

type auditRecorderValue struct {
	recorder audit.Recorder
}

// SetAuditRecorder installs the process-wide Recorder used by the MCP tool
// wrapper. Call once during server startup, before any request handles.
func SetAuditRecorder(r audit.Recorder) {
	if r == nil {
		r = audit.NoopRecorder{}
	}
	auditRecorder.Store(&auditRecorderValue{recorder: r})
}

func currentAuditRecorder() audit.Recorder {
	v := auditRecorder.Load()
	if v == nil {
		return audit.UnwiredRecorder{}
	}
	return v.(*auditRecorderValue).recorder
}

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
//
// The wrapper enforces strict top-level argument keys before dispatch: any
// key not declared in tool.InputSchema.Properties causes a recoverableError.
// Nested keys inside properties are validated separately by the service
// layer (validateNodeProps).
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
		return err
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
		start := clock.Now()
		ctx, span := telemetry.StartSpan(ctx, "mcp.tool."+name,
			trace.WithAttributes(attribute.String("mcp.tool.name", name)),
		)
		defer span.End()

		log := telemetry.L(ctx)
		log.DebugContext(ctx, "mcp.tool.started", slog.String("tool", name))

		// Attach a mutable Scope so resolvers running inside the inner
		// handler can stamp the org/workspace they resolved. The wrapper
		// reads it after the handler returns and feeds it into the audit
		// event so workspace-scoped audit queries see the row.
		ctx = audit.WithScopeBuilder(ctx)
		ctx = audit.WithEntityBuilder(ctx)
		ctx = withAuthorization(ctx)

		res, err := h(ctx, req)
		dur := clock.Since(start)
		telemetry.IncMCPTool(name)

		// Fail closed: a successful result whose handler never passed a
		// membership check is refused here, so a handler that forgets the
		// check cannot return data.
		refused := err == nil && (res == nil || !res.IsError) && !isAuthorized(ctx)
		if refused {
			log.WarnContext(ctx, "mcp.tool.refused",
				slog.String("tool", name),
				slog.String("reason", "no membership check passed"),
			)
			res = permissionDenied()
		}

		switch {
		case err != nil:
			telemetry.IncMCPToolErr(name)
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed")
			log.WarnContext(ctx, "mcp.tool.failed",
				slog.String("tool", name),
				slog.String("status", "failed"),
				slog.Int64("duration_ms", dur.Milliseconds()),
				slog.String("err", err.Error()),
			)
		case res != nil && res.IsError:
			telemetry.IncMCPToolErr(name)
			span.SetStatus(codes.Error, "error_response")
			log.WarnContext(ctx, "mcp.tool.failed",
				slog.String("tool", name),
				slog.String("status", "error_response"),
				slog.Int64("duration_ms", dur.Milliseconds()),
			)
		default:
			span.SetStatus(codes.Ok, "ok")
			log.InfoContext(ctx, "mcp.tool.completed",
				slog.String("tool", name),
				slog.String("status", "ok"),
				slog.Int64("duration_ms", dur.Milliseconds()),
			)
		}
		span.SetAttributes(
			attribute.Int64("mcp.tool.duration_ms", dur.Milliseconds()),
			attribute.Bool("mcp.tool.error", err != nil || (res != nil && res.IsError)),
		)
		recordToolAudit(ctx, name, req, res, err, refused)
		return res, err
	}
}

// recordToolAudit emits transport and semantic audit rows for an MCP tool
// invocation. Outcome is derived from the same signal wrapToolHandler used for
// slog. Failure to record is logged at Warn rather than failing the request:
// the user-visible operation already completed and we do not want audit to
// back-pressure the MCP boundary. Read-class verbs additionally pass through
// the WAL so a transient Yugabyte outage cannot drop them.
func recordToolAudit(ctx context.Context, toolName string, req mcpmcp.CallToolRequest, res *mcpmcp.CallToolResult, runErr error, refused bool) {
	verb, covered := audit.ToolVerb(toolName)
	rec := currentAuditRecorder()
	actor := audit.Actor{Type: audit.ActorUser}
	if uid, found := auth.UserID(ctx); found {
		actor.ID = uid
	}
	actor.RequestID = telemetry.RequestID(ctx)

	outcome := audit.OutcomeOK
	var errInfo *audit.EventError
	switch {
	case refused:
		outcome = audit.OutcomeError
		errInfo = &audit.EventError{Code: "permission_denied", Message: "no membership check passed"}
	case runErr != nil:
		outcome = audit.OutcomeError
		errInfo = &audit.EventError{Code: "tool_error", Message: runErr.Error()}
	case res != nil && res.IsError:
		outcome = audit.OutcomeError
		errInfo = &audit.EventError{Code: "tool_error_response"}
	}

	scope := audit.ScopeFromContext(ctx)
	ev := audit.Event{
		EventID: uuid.Nil,
		Verb:    string(verb),
		Actor:   actor,
		Entity:  audit.Entity{Type: "mcp_tool", Name: toolName},
		Context: audit.EventContext{
			Source:      audit.SourceMCP,
			Tool:        toolName,
			OrgID:       scope.OrgID,
			WorkspaceID: scope.WorkspaceID,
			ScopeID:     scope.ScopeID,
			ParentID:    scope.ParentID,
			RequestID:   telemetry.RequestID(ctx),
			TraceID:     telemetry.TraceID(ctx),
		},
		Outcome: outcome,
		Error:   errInfo,
	}
	invocation := ev
	invocation.Verb = string(audit.VerbMCPToolInvoked)
	if err := rec.Record(ctx, invocation); err != nil {
		telemetry.L(ctx).Warn("audit.record_failed",
			slog.String("tool", toolName),
			slog.String("err", err.Error()),
		)
	}
	if !covered || verb == "" {
		return
	}
	ev.Verb = string(verb)
	entity := audit.EntityFromContext(ctx)
	if entity.ID != uuid.Nil {
		ev.Entity = entity
	}
	if err := rec.Record(ctx, ev); err != nil {
		telemetry.L(ctx).Warn("audit.record_failed",
			slog.String("tool", toolName),
			slog.String("err", err.Error()),
		)
	}
	_ = req // reserved for argument-aware audit enrichment (TACK-179)
}
