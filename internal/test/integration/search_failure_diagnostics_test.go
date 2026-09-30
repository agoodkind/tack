package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/testenv"
)

func captureSearchFailure(t *testing.T, fixture queryFixture, modelID string, containers ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	defer cancel()
	for _, container := range containers {
		evidence, err := testenv.OpenSearchResourceEvidence(ctx, container)
		t.Logf("search failure container evidence: %s error=%v", evidence, err)
	}
	// The typed statistics response omits cgroup fields required for this diagnosis.
	requests := []opensearch.Request{
		opensearchapi.NodesStatsReq{Metric: []string{"jvm", "breaker", "os", "process"}},
		clusterModelRecordRequest{modelID: modelID},
		clusterModelProfileRequest{modelID: modelID},
	}
	for _, request := range requests {
		var raw json.RawMessage
		response, err := opensearch.Do(ctx, fixture.Client.Client, http.MethodGet, request, &raw)
		if err != nil {
			t.Logf("search failure diagnostic request=%T error=%v", request, err)
			continue
		}
		if response == nil {
			t.Logf("search failure diagnostic request=%T returned no response", request)
			continue
		}
		if response.IsError() {
			t.Logf("search failure diagnostic request=%T status=%d error=%v", request, response.StatusCode, opensearch.ParseError(response))
			continue
		}
		t.Logf("search failure diagnostic at=%s request=%T status=%d response=%s", time.Now().UTC().Format(time.RFC3339Nano), request, response.StatusCode, raw)
	}
}
