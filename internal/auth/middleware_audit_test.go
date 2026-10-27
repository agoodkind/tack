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

// devBearerResponse sends one request with a development bearer through
// middleware and returns the status, the decoded JSON body, and whether the
// next handler ran.
func devBearerResponse(t *testing.T, middleware func(http.Handler) http.Handler) (int, map[string]string, bool) {
	t.Helper()
	var nextRan atomic.Bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextRan.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"served"}`))
	})
	server := httptest.NewServer(middleware(next))
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
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON body: %v", err)
	}
	return resp.StatusCode, body, nextRan.Load()
}

// TestDevBearerRefusesRequestWhenAuthEventIsNotRecorded is TACK-583: when
// the recorder refuses the auth.token_used event, the middleware answers 503
// with a JSON error body and does not call the next handler. A middleware
// built later with a refusing recorder does not change the recorder of a
// middleware built earlier, as with two runtime graphs in one process.
func TestDevBearerRefusesRequestWhenAuthEventIsNotRecorded(t *testing.T) {
	working := audit.NewMemoryRecorder()
	served := auth.DevBearer(nil, working)
	refusing := auth.DevBearer(nil, audit.UnwiredRecorder{})

	status, body, nextRan := devBearerResponse(t, refusing)
	if status != http.StatusServiceUnavailable || body["error"] == "" {
		t.Fatalf("refusing recorder: status = %d, body = %v, want 503 with a JSON error", status, body)
	}
	if nextRan {
		t.Fatal("the next handler ran for a request without a recorded auth event")
	}

	status, body, nextRan = devBearerResponse(t, served)
	if status != http.StatusOK || !nextRan || body["status"] != "served" {
		t.Fatalf("working recorder: status = %d, body = %v, next ran = %v, want the served request", status, body, nextRan)
	}
	recorded := 0
	for _, event := range working.Events() {
		if event.Verb == string(audit.VerbAuthTokenUsed) {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("working recorder stored %d auth.token_used events, want 1", recorded)
	}
}
