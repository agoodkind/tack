package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/google/uuid"
)

const (
	testRequestID   = "req-test-123"
	testTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID      = "00f067aa0ba902b7"
	testTraceparent = "00-" + testTraceID + "-" + testSpanID + "-01"
)

func TestRequestLoggerPreservesInboundRequestIDAndTraceContext(t *testing.T) {
	closer, err := setupTracing("")
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("close tracing: %v", err)
		}
	})

	capture := newCaptureHandler()
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestID(r.Context()); got != testRequestID {
			t.Fatalf("request id = %q, want %q", got, testRequestID)
		}
		if got := TraceID(r.Context()); got != testTraceID {
			t.Fatalf("trace id = %q, want %q", got, testTraceID)
		}
		if got := SpanID(r.Context()); got == "" {
			t.Fatal("span id is empty")
		}

		L(r.Context()).InfoContext(r.Context(), "handler.log")
		w.WriteHeader(http.StatusAccepted)
	}))

	request := httptest.NewRequest(http.MethodPost, "/telemetry", nil)
	request.Header.Set(RequestIDHeader, testRequestID)
	request.Header.Set("traceparent", testTraceparent)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get(RequestIDHeader); got != testRequestID {
		t.Fatalf("response request id = %q, want %q", got, testRequestID)
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusAccepted)
	}

	record, ok := capture.find("handler.log")
	if !ok {
		t.Fatal("handler log was not captured")
	}
	if got := record.attrs["request_id"].String(); got != testRequestID {
		t.Fatalf("logged request_id = %q, want %q", got, testRequestID)
	}
	if got := record.attrs["trace_id"].String(); got != testTraceID {
		t.Fatalf("logged trace_id = %q, want %q", got, testTraceID)
	}
	if got := record.attrs["span_id"].String(); got == "" {
		t.Fatal("logged span_id is empty")
	}
}

func TestConnectUnaryInterceptorPreservesInboundRequestID(t *testing.T) {
	closer, err := setupTracing("")
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("close tracing: %v", err)
		}
	})

	request := connect.NewRequest(&testConnectRequest{})
	request.Header().Set(RequestIDHeader, testRequestID)
	request.Header().Set("traceparent", testTraceparent)
	interceptor := ConnectUnaryInterceptor()

	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if got := RequestID(ctx); got != testRequestID {
			t.Fatalf("request id = %q, want %q", got, testRequestID)
		}
		if got := TraceID(ctx); got != testTraceID {
			t.Fatalf("trace id = %q, want %q", got, testTraceID)
		}
		return connect.NewResponse(&testConnectResponse{}), nil
	}

	response, err := interceptor.WrapUnary(next)(context.Background(), request)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if got := response.Header().Get(RequestIDHeader); got != testRequestID {
		t.Fatalf("response request id = %q, want %q", got, testRequestID)
	}
}

func TestConnectUnaryInterceptorGeneratesRequestID(t *testing.T) {
	closer, err := setupTracing("")
	if err != nil {
		t.Fatalf("setup tracing: %v", err)
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Fatalf("close tracing: %v", err)
		}
	})

	request := connect.NewRequest(&testConnectRequest{})
	interceptor := ConnectUnaryInterceptor()

	next := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if _, err := uuid.Parse(RequestID(ctx)); err != nil {
			t.Fatalf("generated request id is not a UUID: %v", err)
		}
		return connect.NewResponse(&testConnectResponse{}), nil
	}

	response, err := interceptor.WrapUnary(next)(context.Background(), request)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if _, err := uuid.Parse(response.Header().Get(RequestIDHeader)); err != nil {
		t.Fatalf("response request id is not a UUID: %v", err)
	}
}

func TestQueryOperation(t *testing.T) {
	tests := map[string]string{
		"":                             "unknown",
		"   ":                          "unknown",
		"select * from users":          "SELECT",
		"\n\tinsert into users values": "INSERT",
		"with rows as (select 1)":      "WITH",
	}

	for sql, want := range tests {
		t.Run(want, func(t *testing.T) {
			if got := queryOperation(sql); got != want {
				t.Fatalf("queryOperation(%q) = %q, want %q", sql, got, want)
			}
		})
	}
}

type testConnectRequest struct{}

type testConnectResponse struct{}
