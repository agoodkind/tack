package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

func TestHealthzAllProbesPass(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"yugabyte": func(context.Context) error { return nil },
		"fdb":      func(context.Context) error { return nil },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

func TestHealthzFailingProbeReturns503(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"yugabyte": func(context.Context) error { return errors.New("down") },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if body := rec.Body.String(); body != "yugabyte unhealthy\n" {
		t.Fatalf("body = %q, want %q", body, "yugabyte unhealthy\\n")
	}
}

func TestHealthzReportsFirstFailingProbeInNameOrder(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"yugabyte": func(context.Context) error { return errors.New("down") },
		"fdb":      func(context.Context) error { return errors.New("down") },
	})

	for i := range 32 {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if body := recorder.Body.String(); body != "fdb unhealthy\n" {
			t.Fatalf("attempt %d body = %q, want %q", i, body, "fdb unhealthy\\n")
		}
	}
}

func TestHealthzStartsHangingProbesConcurrently(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 2)
	newHangingProbe := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			started <- name
			<-ctx.Done()
			return ctx.Err()
		}
	}
	h := newHealthHandler(map[string]func(context.Context) error{
		"fdb":      newHangingProbe("fdb"),
		"yugabyte": newHangingProbe("yugabyte"),
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(requestContext))
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(250 * time.Millisecond):
			cancel()
			<-done
			t.Fatal("handler did not start every probe concurrently")
		}
	}
	cancel()
	<-done
}

func TestHealthzHangingProbeReturnsWithinSharedDeadline(t *testing.T) {
	h := newHealthHandler(map[string]func(context.Context) error{
		"fdb": func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	recorder := httptest.NewRecorder()
	startedAt := time.Now()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second+250*time.Millisecond {
		t.Fatalf("handler returned after %s, want within two seconds", elapsed)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if body := recorder.Body.String(); body != "fdb unhealthy\n" {
		t.Fatalf("body = %q, want %q", body, "fdb unhealthy\\n")
	}
}

func TestHealthzRouteUsesHealthHandler(t *testing.T) {
	healthHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux := buildServeMux(productReadRecorder(t), http.NotFoundHandler(), func(next http.Handler) http.Handler {
		return next
	}, healthHandler)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

// recorderTransactionTimeout limits the duration of each FoundationDB transaction for the test recorder.
const recorderTransactionTimeout = 5 * time.Second

// productReadRecorder returns a request recorder that saves read events in test FoundationDB.
// Each recorder uses a new key prefix. Test cleanup deletes keys under that prefix.
func productReadRecorder(t *testing.T) *mcpadapter.RequestRecorder {
	t.Helper()
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearRecorderPrefix(t, clusterFile, prefix) })
	stores, err := fdbadapter.NewStores(clusterFile, recorderTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("test could not open FoundationDB stores: %v", err)
	}
	return mcpadapter.NewRequestRecorder(stores.AuditReads(clock.Wall{}))
}

// clearRecorderPrefix deletes the keys under prefix and resets the test prefix.
func clearRecorderPrefix(t *testing.T, clusterFile string, prefix []byte) {
	t.Helper()
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(clusterFile, recorderTransactionTimeout)
	if err != nil {
		t.Logf("cleanup could not open FoundationDB: %v", err)
		return
	}
	end := append(bytes.Clone(prefix), 0xFF)
	if _, err := database.Transact(func(transaction fdb.Transaction) (any, error) {
		transaction.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	}); err != nil {
		t.Logf("cleanup could not delete the test keys: %v", err)
	}
}
