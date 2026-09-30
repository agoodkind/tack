package integration

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/testenv"
)

func clusterModelDiagnostics(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID, member string, stoppedAt time.Time) clusterFailureCallbacks {
	t.Helper()
	captured := false
	observe, finish := clusterPredictionDiagnostics(t, cluster, fixture, modelID, member, stoppedAt)
	return clusterFailureCallbacks{Finish: finish, Worker: func(_ error) { observe() }, Public: func(queryError error) {
		observe()
		if captured {
			return
		}
		captured = true
		t.Errorf("public search failed while member %s was stopped: %v", member, queryError)
		captureSearchFailure(t, fixture, modelID, cluster.Members()...)
		t.Logf("availability diagnostic: member=%s stopped_at=%s elapsed=%s public_error=%v backends=%v", member, stoppedAt.UTC().Format(time.RFC3339Nano), time.Since(stoppedAt), queryError, cluster.Backends(t))
		clusterMLResponse(t, fixture, http.MethodGet, clusterModelRecordRequest{modelID: modelID})
		profile, profileStatus := clusterMLResponse(t, fixture, http.MethodGet, clusterModelProfileRequest{modelID: modelID})
		if profileStatus >= http.StatusOK && profileStatus < http.StatusMultipleChoices {
			logClusterModelWorkers(t, profile, modelID, cluster, fixture, member)
		}
		body, err := json.Marshal(struct {
			TextDocs []string `json:"text_docs"`
		}{TextDocs: []string{clusterPhrase}})
		clusterRequire(t, "encode diagnostic inference", err)
		raw, status := clusterMLResponse(t, fixture, http.MethodPost, clusterModelPredictRequest{modelID: modelID, body: body})
		observe()
		clusterMLResponse(t, fixture, http.MethodGet, clusterModelRecordRequest{modelID: modelID})
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			t.Logf("direct inference rejected with status %d", status)
			return
		}
		var response struct {
			Results []struct {
				Output []struct {
					Data struct {
						Response []map[string]float64 `json:"response"`
					} `json:"dataAsMap"`
				} `json:"output"`
			} `json:"inference_results"`
		}
		clusterRequire(t, "decode diagnostic inference", json.Unmarshal(raw, &response))
		if len(response.Results) != 1 || len(response.Results[0].Output) != 1 || len(response.Results[0].Output[0].Data.Response) != 1 {
			t.Fatal("direct inference returned an unexpected result shape")
		}
		weights := response.Results[0].Output[0].Data.Response[0]
		if len(weights) == 0 {
			t.Fatal("direct inference returned no token weights")
		}
		for token, weight := range weights {
			if token == "" || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				t.Fatalf("direct inference returned an invalid weight for %q", token)
			}
		}
		t.Logf("direct inference succeeded during public query rejection with %d finite nonnegative token weights", len(weights))
	}}
}

func logClusterModelWorkers(t *testing.T, raw []byte, modelID string, cluster *testenv.OpenSearchCluster, fixture queryFixture, stoppedMember string) {
	t.Helper()
	var profile struct {
		Nodes map[string]struct {
			Models map[string]struct {
				State       string   `json:"model_state"`
				WorkerNodes []string `json:"worker_nodes"`
			} `json:"models"`
		} `json:"nodes"`
	}
	clusterRequire(t, "decode diagnostic model profile", json.Unmarshal(raw, &profile))
	nodes, err := fixture.Client.Nodes.Info(t.Context(), &opensearchapi.NodesInfoReq{Metrics: []string{"http"}})
	clusterRequire(t, "read diagnostic surviving nodes", err)
	t.Logf("surviving node responses total=%d successful=%d failed=%d", nodes.NodesInfo.Total, nodes.NodesInfo.Successful, nodes.NodesInfo.Failed)
	backends := cluster.Backends(t)
	eligible := map[string]bool{}
	for nodeID, node := range profile.Nodes {
		placement, exists := node.Models[modelID]
		if !exists {
			continue
		}
		t.Logf("model placement node=%s state=%s worker_nodes=%v", nodeID, placement.State, placement.WorkerNodes)
		if placement.State != "DEPLOYED" {
			continue
		}
		for _, workerID := range placement.WorkerNodes {
			worker, exists := nodes.Nodes[workerID]
			if !exists {
				t.Logf("profile worker %s is absent from the surviving node response", workerID)
				continue
			}
			up := backends[cluster.MemberEndpoint(worker.Name)] == "UP"
			valid := worker.Name != stoppedMember && slices.Contains(cluster.Members(), worker.Name) && slices.Contains(worker.Roles, "ml") && up
			eligible[workerID] = valid
			t.Logf("surviving worker id=%s name=%s roles=%v proxy_up=%t eligible=%t", workerID, worker.Name, worker.Roles, up, valid)
		}
	}
	count := 0
	for _, valid := range eligible {
		if valid {
			count++
		}
	}
	t.Logf("model profile identifies %d surviving eligible deployed workers", count)
}

func clusterMLResponse(t *testing.T, fixture queryFixture, method string, request opensearch.Request) ([]byte, int) {
	t.Helper()
	var raw json.RawMessage
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, method, request, &raw)
	clusterRequire(t, "perform diagnostic ML request", err)
	if response == nil {
		t.Fatal("diagnostic ML request returned no response")
	}
	if response.IsError() {
		t.Logf("ML diagnostic request=%T status=%d error=%v", request, response.StatusCode, opensearch.ParseError(response))
		return nil, response.StatusCode
	}
	t.Logf("ML diagnostic at=%s request=%T status=%d response=%s", time.Now().UTC().Format(time.RFC3339Nano), request, response.StatusCode, raw)
	return raw, response.StatusCode
}
