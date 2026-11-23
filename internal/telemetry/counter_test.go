package telemetry

import (
	"encoding/json"
	"expvar"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"
)

func TestRequestMetricsExposesCountWithoutExport(t *testing.T) {
	handler, err := RequestMetrics(noop.NewMeterProvider(), expvar.Handler())
	if err != nil {
		t.Fatal(err)
	}

	before := httpRequests.Value()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	handler.ServeHTTP(response, request)

	var result struct {
		Requests int64 `json:"tack_http_requests_total"`
	}

	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	if result.Requests != before+1 {
		t.Fatalf("expvar request count = %d, want %d", result.Requests, before+1)
	}
}
