package telemetry

import (
	"expvar"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/metric"
)

var httpRequests = expvar.NewInt("tack_http_requests_total")

// RequestMetrics counts HTTP requests in expvar and OpenTelemetry.
//
// [RequestLogger] supplies the request span for metric exemplars.
//
//	handler, err := RequestMetrics(provider, mux)
func RequestMetrics(provider metric.MeterProvider, next http.Handler) (http.Handler, error) {
	counter, err := provider.Meter(tackInstrumentationName).Int64Counter(
		"tack.http.requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Number of HTTP requests received by this Tack instance."),
	)
	if err != nil {
		slog.Error("telemetry.counter_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf("create HTTP request counter: %w", err)
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		httpRequests.Add(1)
		counter.Add(request.Context(), 1)
		next.ServeHTTP(writer, request)
	}), nil
}
