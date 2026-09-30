package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/testenv"
)

type clusterReadyPlacement struct {
	State     string   `json:"model_state"`
	Predictor string   `json:"predictor"`
	Workers   []string `json:"worker_nodes"`
	Targets   []string `json:"target_worker_nodes"`
}

type clusterReadyProfile struct {
	Nodes map[string]struct {
		Models map[string]clusterReadyPlacement `json:"models"`
	} `json:"nodes"`
}

// requireClusterPredictors restores the deployment prerequisite between
// independent member outages. Shard health does not verify model deployment.
func requireClusterPredictors(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID string) {
	t.Helper()
	clusterEventually(t, "restore every pinned predictor before a member outage", func() error {
		members := cluster.Members()
		backends := cluster.Backends(t)
		if len(backends) != len(members) {
			return fmt.Errorf("proxy backends %v do not match members %v", backends, members)
		}
		nodes, err := fixture.Client.Nodes.Info(t.Context(), &opensearchapi.NodesInfoReq{Metrics: []string{"http"}})
		if err != nil {
			return clusterFailure("read predictor node identities", err)
		}
		if nodes.NodesInfo.Failed != 0 || len(nodes.Nodes) != len(members) {
			return fmt.Errorf("predictor nodes total=%d successful=%d failed=%d, want %d members", nodes.NodesInfo.Total, nodes.NodesInfo.Successful, nodes.NodesInfo.Failed, len(members))
		}
		ids := make([]string, 0, len(members))
		names := make([]string, 0, len(members))
		for id, node := range nodes.Nodes {
			if !slices.Contains(members, node.Name) || slices.Contains(names, node.Name) || !slices.Contains(node.Roles, "ml") || backends[cluster.MemberEndpoint(node.Name)] != "UP" {
				return fmt.Errorf("ineligible predictor node id=%s name=%s roles=%v backends=%v", id, node.Name, node.Roles, backends)
			}
			ids = append(ids, id)
			names = append(names, node.Name)
		}
		slices.Sort(ids)
		var raw json.RawMessage
		response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodGet, clusterModelProfileRequest{modelID: modelID}, &raw)
		if err != nil {
			return clusterFailure("read predictor profile", err)
		}
		if response == nil {
			return fmt.Errorf("predictor profile returned no response")
		}
		if response.IsError() {
			return clusterFailure("read predictor profile", opensearch.ParseError(response))
		}
		var profile clusterReadyProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return clusterFailure("decode predictor profile", err)
		}
		if len(profile.Nodes) != len(ids) {
			return fmt.Errorf("predictor profile has %d nodes, want IDs %v: %s", len(profile.Nodes), ids, raw)
		}
		for _, id := range ids {
			placement, exists := profile.Nodes[id].Models[modelID]
			if !exists || placement.State != "DEPLOYED" || placement.Predictor == "" {
				return fmt.Errorf("predictor id=%s has placement %+v, exists=%t", id, placement, exists)
			}
			slices.Sort(placement.Workers)
			slices.Sort(placement.Targets)
			if !slices.Equal(placement.Workers, ids) || !slices.Equal(placement.Targets, ids) {
				return fmt.Errorf("predictor id=%s workers=%v targets=%v, want actual IDs %v", id, placement.Workers, placement.Targets, ids)
			}
		}
		if err := fixture.Adapter.VerifyModel(t.Context(), modelID); err != nil {
			return fmt.Errorf("verify pinned deployed model with actual IDs %v and profile %s: %w", ids, raw, err)
		}
		t.Logf("independent outage prerequisite at=%s model=%s pinned_state=DEPLOYED actual_ids=%v backends=%v profile=%s", time.Now().UTC().Format(time.RFC3339Nano), modelID, ids, backends, raw)
		return nil
	})
}
