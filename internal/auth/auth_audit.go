package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// auditRecorder receives auth-event records. SetAuditRecorder installs it
// once at startup. Until then the unwired recorder refuses and logs every
// auth event.
var auditRecorder atomic.Value // recorderBox

// recorderBox gives every stored recorder one concrete type. [atomic.Value]
// panics when consecutive stores use different dynamic types.
type recorderBox struct {
	recorder audit.Recorder
}

// SetAuditRecorder installs the auth-event audit sink.
func SetAuditRecorder(r audit.Recorder) {
	if r == nil {
		r = audit.NoopRecorder{}
	}
	auditRecorder.Store(recorderBox{recorder: r})
}

func currentRecorder() audit.Recorder {
	box, stored := auditRecorder.Load().(recorderBox)
	if !stored {
		return audit.UnwiredRecorder{}
	}
	return box.recorder
}

// hashBearer returns the first 16 hexadecimal characters of sha256(bearer).
// Audit events use it to correlate one client without storing the token.
func hashBearer(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// emitAuthAudit adds the request fields to ev, validates it, and records it.
// It logs a validation or recording failure and does not fail the request.
func emitAuthAudit(ctx context.Context, r *http.Request, ev audit.Event) {
	rec := currentRecorder()
	ev.Actor.IP = clientIP(r)
	ev.Actor.UserAgent = r.UserAgent()
	ev.Actor.RequestID = telemetry.RequestID(ctx)
	ev.Context.Source = audit.SourceMCP
	ev.Context.RequestID = telemetry.RequestID(ctx)
	ev.Context.TraceID = telemetry.TraceID(ctx)
	if err := audit.ValidateEvent(ev); err != nil {
		slog.ErrorContext(ctx, "auth.audit_event_invalid",
			slog.String("event_id", ev.EventID.String()), slog.String("err", err.Error()))
		return
	}
	if err := rec.Record(ctx, ev); err != nil {
		telemetry.L(ctx).Warn("audit.auth_record_failed", "err", err)
	}
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	return r.RemoteAddr
}
