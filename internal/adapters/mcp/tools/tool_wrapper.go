package tools

import (
	"context"
	"log/slog"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// wrapToolHandler adds structured logging, expvar counters, and audit
// recording to an MCP tool handler. Every tool call logs two messages:
//
//   - mcp.tool.started (Debug) on entry with the tool name
//   - mcp.tool.completed (Info, status=ok) or mcp.tool.failed (Warn,
//     status=failed/error_response) on exit, with duration_ms
//
// The "error_response" status marks a CallToolResult with IsError=true. The
// handler returns a nil Go error for those results, and the client can
// correct the request.
//
// IncMCPTool and IncMCPToolErr update process-global expvars for /debug/vars.
// The closure stores the tool name for each registration.
func wrapToolHandler(name string, h mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		start := clock.Now()
		ctx, span := telemetry.StartSpan(ctx, "mcp.tool."+name,
			trace.WithAttributes(attribute.String("mcp.tool.name", name)),
		)
		defer span.End()

		log := telemetry.L(ctx)
		log.DebugContext(ctx, "mcp.tool.started", slog.String("tool", name))

		// Resolvers inside the handler write the org, workspace, and entity
		// they resolved to these builders. The audit events read them.
		ctx = audit.WithScopeBuilder(ctx)
		ctx = audit.WithEntityBuilder(ctx)
		ctx = withAuthorization(ctx)
		ctx = withToolNote(ctx)
		// A FoundationDB writer commits the change events of a state change
		// in its transaction and marks this slot committed.
		actorID, _ := auth.UserID(ctx)
		ctx = auditintent.WithSlot(ctx, name, actorID)

		res, err := h(ctx, req)
		dur := clock.Since(start)
		telemetry.IncMCPTool(name)

		// Fail closed: the wrapper refuses a successful result when the
		// handler did not pass a membership check.
		refused := err == nil && (res == nil || !res.IsError) && !isAuthorized(ctx)
		if refused {
			log.WarnContext(ctx, "mcp.tool.refused",
				slog.String("tool", name),
				slog.String("reason", "no membership check passed"),
			)
			res = permissionDenied()
		}
		logToolOutcome(ctx, span, name, dur.Milliseconds(), res, err)
		noteToolOutcome(ctx, refused, err, res)
		if !audit.HasReadCollector(ctx) && toolChangesState(name) {
			recordToolResult(ctx, name, res, err, refused)
		}
		return res, err
	}
}

// logToolOutcome logs the end of one tool call and sets its span status.
func logToolOutcome(ctx context.Context, span trace.Span, name string, durationMS int64, res *mcpmcp.CallToolResult, err error) {
	log := telemetry.L(ctx)
	switch {
	case err != nil:
		telemetry.IncMCPToolErr(name)
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed")
		log.WarnContext(ctx, "mcp.tool.failed",
			slog.String("tool", name),
			slog.String("status", "failed"),
			slog.Int64("duration_ms", durationMS),
			slog.String("err", err.Error()),
		)
	case res != nil && res.IsError:
		telemetry.IncMCPToolErr(name)
		span.SetStatus(codes.Error, "error_response")
		log.WarnContext(ctx, "mcp.tool.failed",
			slog.String("tool", name),
			slog.String("status", "error_response"),
			slog.Int64("duration_ms", durationMS),
		)
	default:
		span.SetStatus(codes.Ok, "ok")
		log.InfoContext(ctx, "mcp.tool.completed",
			slog.String("tool", name),
			slog.String("status", "ok"),
			slog.Int64("duration_ms", durationMS),
		)
	}
	span.SetAttributes(
		attribute.Int64("mcp.tool.duration_ms", durationMS),
		attribute.Bool("mcp.tool.error", err != nil || (res != nil && res.IsError)),
	)
}
