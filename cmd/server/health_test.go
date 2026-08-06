package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
