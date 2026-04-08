package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/token"
	"github.com/agoodkind/tack/internal/telemetry"
	"github.com/google/uuid"
)

// TokenValidator is the minimal interface the middleware needs.
type TokenValidator interface {
	Validate(ctx context.Context, raw string) (*token.Token, error)
}

// Bearer returns HTTP middleware that requires a valid API token.
// On success injects the user ID into the request context.
// On failure returns 401 with a JSON body.
func Bearer(tokens TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractBearer(r)
			if raw == "" {
				unauthorized(w, "missing Authorization header")
				return
			}

			t, err := tokens.Validate(r.Context(), raw)
			if err != nil {
				if err == domain.ErrUnauthenticated {
					unauthorized(w, "invalid or expired token")
					return
				}
				telemetry.L(r.Context()).Error("token validate", "err", err)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}

			ctx := WithUser(r.Context(), t.UserID)
			// Attach user_id to the logger so every downstream log line carries it.
			ctx = telemetry.WithLogger(ctx,
				telemetry.L(ctx).With("user_id", t.UserID.String()),
			)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DevBearer is a shim for development: accepts any UUID in the Authorization
// header directly as a user ID, bypassing the token table entirely.
// Only active when env=development.
func DevBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := extractBearer(r)
		if raw == "" {
			unauthorized(w, "missing Authorization header")
			return
		}
		userID, err := uuid.Parse(raw)
		if err != nil {
			unauthorized(w, "dev mode: Authorization header must be a UUID")
			return
		}
		ctx := WithUser(r.Context(), userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
	w.Write([]byte(`{"error":"` + msg + `"}`)) //nolint:errcheck
}
