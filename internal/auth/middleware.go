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
func attachMembership(
	ctx context.Context,
	orgs OrgLister,
	userID uuid.UUID,
) (context.Context, uuid.UUID) {
	if orgs == nil {
		return ctx, uuid.Nil
	}

	ids, err := orgs.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.WarnContext(
			ctx,
			"auth.org_stamp_failed",
			slog.String("user_id", userID.String()),
			slog.String("err", err.Error()),
		)

		return ctx, uuid.Nil
	}

	ctx = WithOrgMembership(ctx, ids)
	if len(ids) == 1 {
		return ctx, ids[0]
	}

	return ctx, uuid.Nil
}

// Bearer returns HTTP middleware that requires a valid API token and stores
// the authenticated user ID in the request context.
//
// It records the accepted token's auth event with recorder, the audit
// recorder for the runtime graph that serves the request. A nil recorder
// rejects every event. The middleware calls next only after successful
// validation and recording.
//
// The middleware returns JSON error responses for these conditions:
//   - A missing or refused token causes HTTP 401.
//   - A token check that cannot determine validity causes HTTP 503 without a
//     call to next.
//   - A recorder that rejects the auth event causes HTTP 503 without a call
//     to next.
func Bearer(
	tokens TokenValidator,
	orgs OrgLister,
	recorder audit.Recorder,
) func(http.Handler) http.Handler {
	recorder = wiredRecorder(recorder)

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
				authUnavailable(w)

				return
			}

			ctx := withAuthenticatedUser(r.Context(), t.UserID)
			ctx, soleOrg := attachMembership(ctx, orgs, t.UserID)
			actor := audit.Actor{
				Type:          audit.ActorUser,
				ID:            t.UserID,
				APITokenLabel: hashBearer(raw),
			}
			event := tokenUsedEvent(
				ctx,
				startedAt,
				actor,
				soleOrg,
				tokenTarget(t.ID, t.Label),
				t.ID,
			)
			if err := emitAuthAudit(ctx, recorder, r, event); err != nil {
				auditUnavailable(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DevBearer accepts a Bearer UUID in the Authorization header as the user ID
// without reading the token table.
//
// The runtime selects it only when ENV=development. Like Bearer, it returns
// HTTP 503 when the recorder rejects the auth event.
//
// Its request handling distinguishes these conditions:
//   - A missing bearer value causes HTTP 401.
//   - A bearer value that does not parse as a UUID causes HTTP 401.
//   - A valid UUID and successful audit recording permit the next handler.
func DevBearer(orgs OrgLister, recorder audit.Recorder) func(http.Handler) http.Handler {
	recorder = wiredRecorder(recorder)

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
			if err := emitAuthAudit(ctx, recorder, r, event); err != nil {
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

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

func unauthorized(w http.ResponseWriter, msg string) {
	writeJSONError(w, http.StatusUnauthorized, msg)
}

// authUnavailable returns HTTP 503 with a JSON body when the token check
// cannot answer. For example, the server may be unable to connect to the
// ledger.
func authUnavailable(w http.ResponseWriter) {
	writeJSONError(w, http.StatusServiceUnavailable, "authentication unavailable")
}

// auditUnavailable answers 503 with a JSON body when the audit recorder did
// not accept the auth event of an authenticated request.
func auditUnavailable(w http.ResponseWriter) {
	writeJSONError(w, http.StatusServiceUnavailable, "audit recording unavailable")
}

func withAuthenticatedUser(ctx context.Context, userID uuid.UUID) context.Context {
	ctx = WithUser(ctx, userID)
	return telemetry.WithTraceLogger(ctx, slog.String("user_id", userID.String()))
}
