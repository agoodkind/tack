package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

// TestDevBearerRefusesRequestWhenAuthEventIsNotRecorded is TACK-583: when
// the recorder refuses the auth.token_used event, the middleware answers 503
// with a JSON error body and does not call the next handler.
func TestDevBearerRefusesRequestWhenAuthEventIsNotRecorded(t *testing.T) {
	auth.SetAuditRecorder(audit.UnwiredRecorder{})
	t.Cleanup(func() { auth.SetAuditRecorder(audit.NoopRecorder{}) })

	var nextRan atomic.Bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextRan.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(auth.DevBearer(nil)(next))
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+uuid.Must(uuid.NewV7()).String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body["error"] == "" {
		t.Fatalf("body is not a JSON error: %v %v", err, body)
	}
	if nextRan.Load() {
		t.Fatal("the next handler ran for a request without a recorded auth event")
	}
}
