package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// wiredRecorder returns recorder, or the unwired recorder when the caller
// passed nil. The unwired recorder refuses and logs every auth event, and the
// middleware then refuses each request.
func wiredRecorder(recorder audit.Recorder) audit.Recorder {
	if recorder == nil {
		return audit.UnwiredRecorder{}
	}
	return recorder
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

// emitAuthAudit adds the request fields to ev, validates it, and records it
// through rec.
// It records an event that fails validation, and the audit-consumer stores
// that event in the dead-letter table for repair. It logs a validation failure
// and continues. It returns an error when the recorder refuses the event, and
// the middleware then refuses the request instead of serving it unaudited.
func emitAuthAudit(ctx context.Context, rec audit.Recorder, r *http.Request, ev audit.Event) error {
	ev.Actor.IP = clientIP(r)
	ev.Actor.UserAgent = r.UserAgent()
	ev.Actor.RequestID = telemetry.RequestID(ctx)
	ev.Context.Source = audit.SourceMCP
	ev.Context.RequestID = telemetry.RequestID(ctx)
	ev.Context.TraceID = telemetry.TraceID(ctx)
	if err := audit.ValidateEvent(ev); err != nil {
		slog.ErrorContext(ctx, "auth.audit_event_invalid",
			slog.String("event_id", ev.EventID.String()), slog.String("err", err.Error()))
	}
	if err := rec.Record(ctx, ev); err != nil {
		slog.ErrorContext(ctx, "audit.auth_record_failed",
			slog.String("verb", ev.Verb), slog.String("err", err.Error()))
		return fmt.Errorf("record auth event %s: %w", ev.Verb, err)
	}
	return nil
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
