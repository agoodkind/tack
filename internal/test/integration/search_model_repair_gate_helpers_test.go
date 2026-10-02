package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
)

// repairEnabledVariable turns the model repair loop on in a Tack process.
const repairEnabledVariable = "OPENSEARCH_MODEL_REPAIR_ENABLED"

// buildRepairingQueryGraph builds the cluster test graph and starts its
// production model repair loop. The loop deploys only when the test set
// OPENSEARCH_MODEL_REPAIR_ENABLED to true; the production default is false.
func buildRepairingQueryGraph(t *testing.T, cfg *config.Config) *appruntime.Graph {
	t.Helper()
	graph := buildQueryGraph(t, cfg)
	graph.StartSearchModelRepair(t.Context())
	return graph
}

// deployTasksSinceRequest counts the DEPLOY_MODEL tasks of one model created
// at or after a time, in every state.
type deployTasksSinceRequest struct{ body []byte }

func (request deployTasksSinceRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/tasks/_search", bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("build deploy task count request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

// disableNativeRedeploy turns ML Commons automatic redeploy off in the
// disposable cluster. Every later deploy task comes from Tack.
func disableNativeRedeploy(t *testing.T, fixture queryFixture) {
	t.Helper()
	body := []byte(`{"persistent":{"plugins.ml_commons.model_auto_redeploy.enable":false}}`)
	var result json.RawMessage
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodPut, clusterSettingsRequest{body: body}, &result)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("disable native automatic redeploy: response %v err %v body %s", response, err, result)
	}
}

// deployTasksSince returns the number of DEPLOY_MODEL tasks of modelID that
// the real task index records with a create time at or after since.
func deployTasksSince(t *testing.T, fixture queryFixture, modelID string, since time.Time) int {
	t.Helper()
	body := fmt.Appendf(nil, `{"size":0,"track_total_hits":true,"query":{"bool":{"filter":[`+
		`{"term":{"model_id":%q}},{"term":{"task_type":"DEPLOY_MODEL"}},`+
		`{"range":{"create_time":{"gte":%d}}}]}}}`, modelID, since.UnixMilli())
	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodPost, deployTasksSinceRequest{body: body}, &result)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("count deploy tasks of model %s since %s: response %v err %v", modelID, since, response, err)
	}
	return result.Hits.Total.Value
}
