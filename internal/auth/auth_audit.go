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

// wiredRecorder returns recorder, or an unwired recorder when the caller
// passes nil. The unwired recorder logs and rejects every auth event. The
// middleware rejects an otherwise accepted request when recording fails.
func wiredRecorder(recorder audit.Recorder) audit.Recorder {
	if recorder == nil {
		return audit.UnwiredRecorder{}
	}

	return recorder
}

// hashBearer returns an empty string when raw is empty. Otherwise, it
// returns the first 16 hexadecimal characters of SHA-256(raw). Audit events
// use this value to correlate requests with the same token without storing
// the token.
func hashBearer(raw string) string {
	if raw == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(raw))

	return hex.EncodeToString(sum[:8])
}

// emitAuthAudit adds request fields to ev and validates it before calling
// rec.Record. It logs validation failures and still attempts to record the
// event. With Kafka recording, the audit-consumer stores invalid events in
// the dead-letter table for repair. If rec.Record fails, emitAuthAudit
// returns an error and the middleware rejects the request.
func emitAuthAudit(ctx context.Context, rec audit.Recorder, r *http.Request, ev audit.Event) error {
	ev.Actor.IP = clientIP(r)
	ev.Actor.UserAgent = r.UserAgent()
	ev.Actor.RequestID = telemetry.RequestID(ctx)
	ev.Context.Source = audit.SourceMCP
	ev.Context.RequestID = telemetry.RequestID(ctx)
	ev.Context.TraceID = telemetry.TraceID(ctx)
	if err := audit.ValidateEvent(ev); err != nil {
		slog.ErrorContext(
			ctx,
			"auth.audit_event_invalid",
			slog.String("event_id", ev.EventID.String()),
			slog.String("err", err.Error()),
		)
	}

	if err := rec.Record(ctx, ev); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.auth_record_failed",
			slog.String("verb", ev.Verb),
			slog.String("err", err.Error()),
		)

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
