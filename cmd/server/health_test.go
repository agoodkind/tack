package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	mux := buildServeMux(http.NotFoundHandler(), func(next http.Handler) http.Handler {
		return next
	}, healthHandler)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
