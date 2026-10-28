package integration_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

// proxyErrorUnavailableText is the exact tack_search response when the search engine is unavailable.
const proxyErrorUnavailableText = "Search is temporarily unavailable."

// TestSearchClusterProxyServerError verifies that tack_search returns exactly
// "Search is temporarily unavailable." when Traefik answers model predict requests
// with HTTP 500 and a plain text body. It also checks the proxy access log for
// an HTTP 500 predict response for the serving model.
func TestSearchClusterProxyServerError(t *testing.T) {
	cluster := integration.StartSearchTestCluster(t, 1)
	fixture, spec := integration.NewClusterQueryFixture(t, cluster.Fixture)
	defer integration.CaptureClusterTestFailure(t, cluster, fixture, spec.Model.ID)
	integration.RequireSearchable(t, fixture, []uuid.UUID{integration.ClusterNodeWriter(t, fixture)()})
	cluster.RefusePath(t, predictPathPrefix)
	_, err := integration.TrySearch(fixture.Harness, integration.ClusterPhrase, "")
	if err == nil || err.Error() != proxyErrorUnavailableText {
		t.Fatalf("tack_search with the proxy refusing predictions returned error %v, want %q", err, proxyErrorUnavailableText)
	}
	integration.ClusterEventually(t, "read the proxy's HTTP 500 prediction response", func() error {
		for _, response := range cluster.PredictionResponses(t, spec.Model.ID) {
			if response.Status == http.StatusInternalServerError {
				t.Logf("proxy prediction response backend=%s status=%d origin_status=%d", response.Backend, response.Status, response.OriginStatus)
				return nil
			}
		}
		return errors.New("the proxy access log has no HTTP 500 prediction response")
	})
}
