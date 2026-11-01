//go:build runtimeaudit

package integration

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/service"
)

// QueryService builds the production search service over the fixture's stores
// and adapter with the wall clock and the given session deadlines.
func QueryService(t *testing.T, fixture QueryFixture, responseBytes int, idle, absolute time.Duration) *service.SearchQueryService {
	t.Helper()
	return queryService(t, fixture, responseBytes, idle, absolute)
}

// ServiceRequest returns a search request of the first actor of the first
// fixture workspace.
func ServiceRequest(t *testing.T, fixture QueryFixture, text string) service.SearchRequest {
	t.Helper()
	return serviceRequest(t, fixture, text)
}
