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
// recording to an MCP tool handler. Its closure stores the tool name for
// each registration. After its recorder check, it logs mcp.tool.started at
// Debug before the handler runs. The start log call adds only the tool
// attribute.
//
// After the handler returns, it logs tool and duration_ms with one of
// these completion messages:
//
//   - mcp.tool.completed uses Info with status=ok for a successful result.
//   - mcp.tool.failed uses Warn with status=failed for a Go error, or
//     status=error_response for IsError=true with a nil Go error.
//
// IncMCPTool and IncMCPToolErr update process-global expvar counters that
// /debug/vars exposes. An error response can report an internal server
// failure as well as a request that the client can correct.
func wrapToolHandler(name string, h mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		start := clock.Now()
		ctx, span := telemetry.StartSpan(
			ctx,
			"mcp.tool."+name,
			trace.WithAttributes(attribute.String("mcp.tool.name", name)),
		)
		defer span.End()
		// A state-changing call outside a read collector uses the recorder
		// on its context for its result event. If the context does not have
		// a recorder, the wrapper returns before running the handler or
		// allowing it to change data.
		recordsResult := !audit.HasReadCollector(ctx) && toolChangesState(name)
		var recorder audit.Recorder
		if recordsResult {
			required, err := requireAuditRecorder(ctx)
			if err != nil {
				logRecorderMissing(ctx, "tool:"+name)
				span.RecordError(err)
				span.SetStatus(codes.Error, "audit_recorder_missing")

				return nil, err
			}

			recorder = required
		}

		log := telemetry.L(ctx)
		log.DebugContext(ctx, "mcp.tool.started", slog.String("tool", name))

		// Resolvers store the organization, workspace, and entity they
		// resolve in these context builders for audit event construction.
		ctx = audit.WithScopeBuilder(ctx)
		ctx = audit.WithEntityBuilder(ctx)
		ctx = withAuthorization(ctx)
		ctx = withToolNote(ctx)
		// A FoundationDB writer commits change events in its transaction,
		// then marks this request slot committed.
		actorID, _ := auth.UserID(ctx)
		ctx = auditintent.WithSlot(ctx, name, actorID)

		res, err := h(ctx, req)
		dur := clock.Since(start)
		telemetry.IncMCPTool(name)

		// The wrapper refuses a successful result when the handler did not
		// mark a successful membership check.
		refused := err == nil && (res == nil || !res.IsError) && !isAuthorized(ctx)
		if refused {
			log.WarnContext(
				ctx,
				"mcp.tool.refused",
				slog.String("tool", name),
				slog.String("reason", "no membership check passed"),
			)
			res = permissionDenied()
		}

		logToolOutcome(ctx, span, name, dur.Milliseconds(), res, err)
		noteToolOutcome(ctx, refused, err, res)
		if recordsResult {
			recordToolResult(ctx, recorder, name, res, err, refused)
		}

		return res, err
	}
}

// logToolOutcome logs the end of one tool call and sets its span status.
func logToolOutcome(
	ctx context.Context,
	span trace.Span,
	name string,
	durationMS int64,
	res *mcpmcp.CallToolResult,
	err error,
) {
	log := telemetry.L(ctx)
	switch {
	case err != nil:
		telemetry.IncMCPToolErr(name)
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed")
		log.WarnContext(
			ctx,
			"mcp.tool.failed",
			slog.String("tool", name),
			slog.String("status", "failed"),
			slog.Int64("duration_ms", durationMS),
			slog.String("err", err.Error()),
		)
	case res != nil && res.IsError:
		telemetry.IncMCPToolErr(name)
		span.SetStatus(codes.Error, "error_response")
		log.WarnContext(
			ctx,
			"mcp.tool.failed",
			slog.String("tool", name),
			slog.String("status", "error_response"),
			slog.Int64("duration_ms", durationMS),
		)
	default:
		span.SetStatus(codes.Ok, "ok")
		log.InfoContext(
			ctx,
			"mcp.tool.completed",
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
