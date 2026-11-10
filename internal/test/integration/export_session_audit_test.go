//go:build runtimeaudit

package integration

import (
	"net/http"
	"testing"
	"time"

	"goodkind.io/tack/internal/service"
)

func MCPHandler(harness *MCPHarness) http.Handler {
	return harness.handler
}

func QueryService(t *testing.T, fixture QueryFixture, responseBytes int, idle, absolute time.Duration) *service.SearchQueryService {
	t.Helper()
	return queryService(t, fixture, responseBytes, idle, absolute)
}

func ServiceRequest(t *testing.T, fixture QueryFixture, text string) service.SearchRequest {
	t.Helper()
	return serviceRequest(t, fixture, text)
}
