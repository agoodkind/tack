package tools

import (
	"context"
	"errors"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

type auditRecorderKey struct{}

// WithAuditRecorder returns a context that gives the wrapped tool handlers
// and resource handlers recorder as their audit sink. The MCP handler of each
// runtime graph attaches the recorder of that graph to every request.
func WithAuditRecorder(ctx context.Context, recorder audit.Recorder) context.Context {
	return context.WithValue(ctx, auditRecorderKey{}, recorder)
}

// errAuditRecorderMissing is the refusal of a tool call or resource read that
// must record an audit event when the request context does not have an audit
// recorder.
var errAuditRecorderMissing = errors.New("the request context does not have an audit recorder")

// requireAuditRecorder returns the recorder on ctx, or errAuditRecorderMissing
// when ctx does not have one. The caller that refuses the request logs the
// refusal.
func requireAuditRecorder(ctx context.Context) (audit.Recorder, error) {
	recorder, _ := ctx.Value(auditRecorderKey{}).(audit.Recorder)
	if recorder == nil {
		return nil, errAuditRecorderMissing
	}
	return recorder, nil
}

// logRecorderMissing logs the refusal of one request that has no audit
// recorder on its context.
func logRecorderMissing(ctx context.Context, request string) {
	slog.ErrorContext(ctx, "mcp.audit_recorder_missing",
		slog.String("request", request), slog.String("err", errAuditRecorderMissing.Error()))
}
