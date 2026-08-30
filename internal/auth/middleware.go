package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/telemetry"
)

// auditRecorder receives auth-event records. SetAuditRecorder installs it
// once at startup; until then every auth event is refused and logged, so a
// missed installation is visible rather than silent.
var auditRecorder atomic.Value // recorderBox

// recorderBox gives every stored recorder one concrete type: [atomic.Value]
// panics when consecutive stores carry different dynamic types, which two
// recorder implementations otherwise would.
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
	v := auditRecorder.Load()
	if v == nil {
		return audit.UnwiredRecorder{}
	}
	return v.(recorderBox).recorder
}

// hashBearer returns a short prefix of sha256(bearer) for audit fingerprinting
// without ever exposing the raw token. 16 hex chars = 8 bytes of entropy,
// plenty to correlate the same client across events without aiding theft.
func hashBearer(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func emitAuthAudit(ctx context.Context, r *http.Request, ev audit.Event) {
	rec := currentRecorder()
	ev.Actor.IP = clientIP(r)
	ev.Actor.UserAgent = r.UserAgent()
	ev.Actor.RequestID = telemetry.RequestID(ctx)
	ev.Context.Source = audit.SourceMCP
	ev.Context.RequestID = telemetry.RequestID(ctx)
	ev.Context.TraceID = telemetry.TraceID(ctx)
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

// TokenValidator is the minimal interface the middleware needs.
type TokenValidator interface {
	Validate(ctx context.Context, raw string) (*token.Token, error)
}

// OrgLister resolves the orgs a user belongs to, so auth events carry the
// actor's org instead of the nil org (TACK-461).
type OrgLister interface {
	ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// actorOrg returns the actor's sole org, and uuid.Nil when the actor belongs
// to zero or several orgs: an auth event fires before any workspace names an
// org, so the only honest stamp is a membership that admits no other answer.
// A lookup failure also stamps nil, because enriching the audit context must
// never fail the request that a working recorder would still record.
func actorOrg(ctx context.Context, orgs OrgLister, userID uuid.UUID) uuid.UUID {
	if orgs == nil {
		return uuid.Nil
	}
	ids, err := orgs.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "auth.org_stamp_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return uuid.Nil
	}
	if len(ids) == 1 {
		return ids[0]
	}
	return uuid.Nil
}

// Bearer returns HTTP middleware that requires a valid API token.
// On success injects the user ID into the request context.
// On failure returns 401 with a JSON body.
func Bearer(tokens TokenValidator, orgs OrgLister) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractBearer(r)
			if raw == "" {
				emitAuthAudit(r.Context(), r, audit.Event{
					EventID: uuid.Nil,
					Verb:    string(audit.VerbAuthLoginFailed),
					Actor:   audit.Actor{Type: audit.ActorToken},
					Entity:  audit.Entity{Type: "auth", Name: "missing_authorization"},
					Outcome: audit.OutcomeError,
				})
				unauthorized(w, "missing Authorization header")
				return
			}

			t, err := tokens.Validate(r.Context(), raw)
			if err != nil {
				if err == domain.ErrUnauthenticated {
					emitAuthAudit(r.Context(), r, audit.Event{
						EventID: uuid.Nil,
						Verb:    string(audit.VerbAuthTokenRejected),
						Actor:   audit.Actor{Type: audit.ActorToken, APITokenLabel: hashBearer(raw)},
						Entity:  audit.Entity{Type: "auth", Name: "token_invalid"},
						Outcome: audit.OutcomeError,
					})
					unauthorized(w, "invalid or expired token")
					return
				}
				telemetry.L(r.Context()).Error("token validate", "err", err)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}

			ctx := withAuthenticatedUser(r.Context(), t.UserID)
			used := audit.Event{
				EventID: uuid.Nil,
				Verb:    string(audit.VerbAuthTokenUsed),
				Actor:   audit.Actor{Type: audit.ActorUser, ID: t.UserID, APITokenLabel: hashBearer(raw)},
				Entity:  audit.Entity{Type: "auth", ID: t.UserID, Name: "token_accepted"},
				Outcome: audit.OutcomeOK,
			}
			used.Context.OrgID = actorOrg(ctx, orgs, t.UserID)
			emitAuthAudit(ctx, r, used)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DevBearer is a shim for development: accepts any UUID in the Authorization
// header directly as a user ID, bypassing the token table entirely.
// Only active when env=development.
func DevBearer(orgs OrgLister) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractBearer(r)
			if raw == "" {
				emitAuthAudit(r.Context(), r, audit.Event{
					EventID: uuid.Nil,
					Verb:    string(audit.VerbAuthLoginFailed),
					Actor:   audit.Actor{Type: audit.ActorToken},
					Entity:  audit.Entity{Type: "auth", Name: "dev_missing_authorization"},
					Outcome: audit.OutcomeError,
				})
				unauthorized(w, "missing Authorization header")
				return
			}
			userID, err := uuid.Parse(raw)
			if err != nil {
				emitAuthAudit(r.Context(), r, audit.Event{
					EventID: uuid.Nil,
					Verb:    string(audit.VerbAuthTokenRejected),
					Actor:   audit.Actor{Type: audit.ActorToken},
					Entity:  audit.Entity{Type: "auth", Name: "dev_bearer_not_uuid"},
					Outcome: audit.OutcomeError,
				})
				unauthorized(w, "dev mode: Authorization header must be a UUID")
				return
			}
			ctx := withAuthenticatedUser(r.Context(), userID)
			used := audit.Event{
				EventID: uuid.Nil,
				Verb:    string(audit.VerbAuthTokenUsed),
				Actor:   audit.Actor{Type: audit.ActorUser, ID: userID, APITokenLabel: "dev"},
				Entity:  audit.Entity{Type: "auth", ID: userID, Name: "dev_bearer_accepted"},
				Outcome: audit.OutcomeOK,
			}
			used.Context.OrgID = actorOrg(ctx, orgs, userID)
			emitAuthAudit(ctx, r, used)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

func withAuthenticatedUser(ctx context.Context, userID uuid.UUID) context.Context {
	ctx = WithUser(ctx, userID)
	return telemetry.WithTraceLogger(ctx, slog.String("user_id", userID.String()))
}
