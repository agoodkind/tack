package tools

import (
	"context"
	"errors"
	"fmt"
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

// requireAuditRecorder returns the recorder on ctx. It logs and returns an
// error when ctx does not have one.
func requireAuditRecorder(ctx context.Context) (audit.Recorder, error) {
	recorder, _ := ctx.Value(auditRecorderKey{}).(audit.Recorder)
	if recorder == nil {
		slog.ErrorContext(ctx, "mcp.audit_recorder_missing", slog.String("err", errAuditRecorderMissing.Error()))
		return nil, fmt.Errorf("mcp request: %w", errAuditRecorderMissing)
	}
	return recorder, nil
}
