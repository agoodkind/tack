package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	oteltrace "go.opentelemetry.io/otel/trace"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"goodkind.io/gklog/telemetry"
	"google.golang.org/grpc"
)

type metricReceiver struct {
	collectorpb.UnimplementedMetricsServiceServer
	exports chan *collectorpb.ExportMetricsServiceRequest
}

func (receiver *metricReceiver) Export(
	_ context.Context,
	request *collectorpb.ExportMetricsServiceRequest,
) (*collectorpb.ExportMetricsServiceResponse, error) {
	select {
	case receiver.exports <- request:
	default:
	}

	return &collectorpb.ExportMetricsServiceResponse{}, nil
}

func startMetricReceiver(t *testing.T) (*metricReceiver, string) {
	t.Helper()

	receiver := &metricReceiver{exports: make(chan *collectorpb.ExportMetricsServiceRequest, 1)}
	grpcServer := grpc.NewServer()
	collectorpb.RegisterMetricsServiceServer(grpcServer, receiver)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	return receiver, "http://" + listener.Addr().String()
}

func TestServerTelemetryExportsRequest(t *testing.T) {
	t.Setenv("GKLOG_LOG_ENABLED", "true")
	t.Setenv("GKLOG_LOG_LEVEL", "ERROR")
	t.Setenv("GKLOG_TRACE_ENABLED", "true")
	t.Setenv("GKLOG_TRACE_SAMPLE_RATIO", "1")
	t.Setenv("GKLOG_METRIC_EXPORT_ENABLED", "true")
	t.Setenv("GKLOG_CPU_PROFILE_ENABLED", "false")
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "trace_based")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "100")

	defer slog.SetDefault(slog.Default())
	defer otel.SetTracerProvider(otel.GetTracerProvider())
	defer otel.SetMeterProvider(otel.GetMeterProvider())
	defer otel.SetTextMapPropagator(otel.GetTextMapPropagator())
	receiver, endpoint := startMetricReceiver(t)
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	server, err := SetupServer(LogConfig{JSONFile: logPath, DisableStdout: true}, "", endpoint)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	}()

	span := serveLoggedRequest(t)
	instance := server.Status().InstanceID
	checkRequestLog(t, logPath, instance, span)
	select {
	case export := <-receiver.exports:
		checkRequestExport(t, export, instance, span)
	case <-time.After(10 * time.Second):
		t.Fatal("collector did not receive the request metric")
	}

	settings := telemetry.TraceSettings{Enabled: true, SampleRatio: 0}
	if err := server.Tracing.Update(settings); err != nil {
		t.Fatal(err)
	}

	_, next := StartSpan(t.Context(), "next")
	defer next.End()
	if !span.IsSampled() || next.IsRecording() {
		t.Fatal("expected only the request before the update to be sampled")
	}
}

func serveLoggedRequest(t *testing.T) oteltrace.SpanContext {
	t.Helper()

	var span oteltrace.SpanContext
	recordError := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		span = oteltrace.SpanContextFromContext(r.Context())
		L(r.Context()).ErrorContext(r.Context(), "test.record")
	})
	handler, err := RequestMetrics(otel.GetMeterProvider(), recordError)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	RequestLogger(handler).ServeHTTP(httptest.NewRecorder(), request)

	return span
}

func checkRequestLog(t *testing.T, logPath, instance string, span oteltrace.SpanContext) {
	t.Helper()
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	var record struct {
		Message  string `json:"msg"`
		Trace    string `json:"trace_id"`
		Instance string `json:"service.instance.id"`
	}
	if err := json.Unmarshal(logBytes, &record); err != nil {
		t.Fatalf("expected a single ERROR record: %v", err)
	}

	if record.Message != "test.record" || record.Trace != span.TraceID().String() ||
		record.Instance == "" || record.Instance != instance {
		t.Fatalf("unexpected request log: %+v", record)
	}
}

func checkRequestExport(
	t *testing.T,
	export *collectorpb.ExportMetricsServiceRequest,
	instance string,
	span oteltrace.SpanContext,
) {
	t.Helper()
	if len(export.ResourceMetrics) != 1 {
		t.Fatalf("expected metrics from one resource, got %v", export.ResourceMetrics)
	}

	resource := export.ResourceMetrics[0]
	var instanceID string

	for _, attr := range resource.GetResource().GetAttributes() {
		if attr.GetKey() == "service.instance.id" {
			instanceID = attr.GetValue().GetStringValue()
		}
	}

	if instanceID != instance {
		t.Fatalf("metric instance = %q, want %q", instanceID, instance)
	}

	if len(resource.ScopeMetrics) != 1 || len(resource.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("expected the request counter, got %v", resource.ScopeMetrics)
	}

	metric := resource.ScopeMetrics[0].Metrics[0]
	if metric.Name != "tack.http.requests" {
		t.Fatalf("unexpected metric %q", metric.Name)
	}

	points := metric.GetSum().GetDataPoints()
	if len(points) != 1 || points[0].GetAsInt() != 1 || len(points[0].GetExemplars()) != 1 {
		t.Fatalf("unexpected collector measurements: %v", points)
	}

	sample := points[0].GetExemplars()[0]
	traceID, spanID := span.TraceID(), span.SpanID()
	if !bytes.Equal(sample.GetTraceId(), traceID[:]) ||
		!bytes.Equal(sample.GetSpanId(), spanID[:]) {
		t.Fatalf("unexpected exemplar IDs: %x/%x", sample.GetTraceId(), sample.GetSpanId())
	}
}

func TestServerTelemetryRequiresProfileBackend(t *testing.T) {
	t.Setenv("GKLOG_CPU_PROFILE_ENABLED", "true")
	_, err := SetupServer(LogConfig{}, "", "")
	if !errors.Is(err, telemetry.ErrBackendUnavailable) {
		t.Fatalf("expected missing profiling backend error, got %v", err)
	}
}
