package tools

import (
	"context"
	"errors"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

type auditRecorderKey struct{}

// WithAuditRecorder attaches recorder to ctx for wrapped tool handlers and
// resource handlers.
//
// Each MCP handler attaches the recorder from its runtime graph to every
// request context.
//
//	ctx = WithAuditRecorder(ctx, recorder)
func WithAuditRecorder(ctx context.Context, recorder audit.Recorder) context.Context {
	return context.WithValue(ctx, auditRecorderKey{}, recorder)
}

// errAuditRecorderMissing is the refusal of a tool call or resource read
// that must record an audit event when the request context does not have an
// audit recorder.
var errAuditRecorderMissing = errors.New("the request context does not have an audit recorder")

// requireAuditRecorder returns the recorder on ctx, or
// errAuditRecorderMissing when ctx does not have one. The caller that
// refuses the request logs the refusal.
func requireAuditRecorder(ctx context.Context) (audit.Recorder, error) {
	recorder, _ := ctx.Value(auditRecorderKey{}).(audit.Recorder)
	if recorder == nil {
		return nil, errAuditRecorderMissing
	}

	return recorder, nil
}

// logRecorderMissing logs a request refusal when its context does not
// contain an audit recorder.
func logRecorderMissing(ctx context.Context, request string) {
	slog.ErrorContext(
		ctx,
		"mcp.audit_recorder_missing",
		slog.String("request", request),
		slog.String("err", errAuditRecorderMissing.Error()),
	)
}
