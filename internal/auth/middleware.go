package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/telemetry"
)

// TokenValidator is the minimal interface the middleware needs.
type TokenValidator interface {
	Validate(ctx context.Context, raw string) (*token.Token, error)
}

// OrgLister resolves the orgs a user belongs to, so auth events carry the
// actor's org instead of the nil org (TACK-461).
type OrgLister interface {
	ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// attachMembership reads the actor's org memberships once per request,
// attaches the set to the context for the handlers behind the middleware,
// and returns the actor's sole org for the auth event. It returns [uuid.Nil]
// when the actor belongs to zero or several orgs, and the auth event then
// uses the system org. A lookup failure attaches nothing and returns
// [uuid.Nil] without failing the request. The MCP handler then makes its own
// lookup and fails closed (TACK-503).
func attachMembership(ctx context.Context, orgs OrgLister, userID uuid.UUID) (context.Context, uuid.UUID) {
	if orgs == nil {
		return ctx, uuid.Nil
	}
	ids, err := orgs.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "auth.org_stamp_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return ctx, uuid.Nil
	}
	ctx = WithOrgMembership(ctx, ids)
	if len(ids) == 1 {
		return ctx, ids[0]
	}
	return ctx, uuid.Nil
}

// Bearer returns HTTP middleware that requires a valid API token.
// On success injects the user ID into the request context.
// On failure returns 401 with a JSON body.
// When the auth event cannot be recorded, it returns 503 with a JSON body and
// does not call next.
func Bearer(tokens TokenValidator, orgs OrgLister) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := clock.Now()
			raw := extractBearer(r)
			if raw == "" {
				noteRejection(r.Context(), RejectionMissingAuthorization)
				unauthorized(w, "missing Authorization header")
				return
			}

			t, err := tokens.Validate(r.Context(), raw)
			if err != nil {
				if isUnauthenticated(err) {
					noteRejection(r.Context(), RejectionBearerRefused)
					unauthorized(w, "invalid or expired token")
					return
				}
				noteRejection(r.Context(), RejectionBearerCheckFailed)
				telemetry.L(r.Context()).Error("token validate", "err", err)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}

			ctx := withAuthenticatedUser(r.Context(), t.UserID)
			ctx, soleOrg := attachMembership(ctx, orgs, t.UserID)
			actor := audit.Actor{Type: audit.ActorUser, ID: t.UserID, APITokenLabel: hashBearer(raw)}
			event := tokenUsedEvent(ctx, startedAt, actor, soleOrg, tokenTarget(t.ID, t.Label), t.ID)
			if err := emitAuthAudit(ctx, r, event); err != nil {
				auditUnavailable(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DevBearer is a shim for development: accepts any UUID in the Authorization
// header directly as a user ID, bypassing the token table entirely.
// Only active when env=development. It refuses a request with 503 when the
// auth event cannot be recorded, as Bearer does.
func DevBearer(orgs OrgLister) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := clock.Now()
			raw := extractBearer(r)
			if raw == "" {
				noteRejection(r.Context(), RejectionMissingAuthorization)
				unauthorized(w, "missing Authorization header")
				return
			}
			userID, err := uuid.Parse(raw)
			if err != nil {
				noteRejection(r.Context(), RejectionDevIdentityInvalid)
				unauthorized(w, "dev mode: Authorization header must be a UUID")
				return
			}
			ctx := withAuthenticatedUser(r.Context(), userID)
			ctx, soleOrg := attachMembership(ctx, orgs, userID)
			actor := audit.Actor{Type: audit.ActorUser, ID: userID, APITokenLabel: "dev"}
			event := tokenUsedEvent(ctx, startedAt, actor, soleOrg, userTarget(userID), uuid.Nil)
			if err := emitAuthAudit(ctx, r, event); err != nil {
				auditUnavailable(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isUnauthenticated reports whether a token lookup refused the token, as
// distinct from failing to answer, through any wrapping a cache added.
func isUnauthenticated(err error) bool {
	return errors.Is(err, domain.ErrUnauthenticated)
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

// auditUnavailable answers 503 with a JSON body when the audit recorder did
// not accept the auth event of an authenticated request.
func auditUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"audit recording unavailable"}`))
}

func withAuthenticatedUser(ctx context.Context, userID uuid.UUID) context.Context {
	ctx = WithUser(ctx, userID)
	return telemetry.WithTraceLogger(ctx, slog.String("user_id", userID.String()))
}
