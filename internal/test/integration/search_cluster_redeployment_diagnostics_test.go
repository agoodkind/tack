package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/testenv"
)

func captureClusterPartialDeployment(t *testing.T, cluster *testenv.OpenSearchCluster, fixture queryFixture, modelID string, captured *bool) {
	t.Helper()
	if *captured {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	defer cancel()
	var raw json.RawMessage
	response, err := opensearch.Do(ctx, fixture.Client.Client, http.MethodGet, clusterModelRecordRequest{modelID: modelID}, &raw)
	if err != nil {
		t.Logf("partial deployment model record request error=%v", err)
		return
	}
	if response == nil {
		t.Log("partial deployment model record request returned no response")
		return
	}
	if response.IsError() {
		t.Logf("partial deployment model record status=%d error=%v", response.StatusCode, opensearch.ParseError(response))
		return
	}
	var record struct {
		State string `json:"model_state"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Logf("partial deployment model record decode error=%v", err)
		return
	}
	if record.State != "PARTIALLY_DEPLOYED" {
		return
	}
	*captured = true
	t.Logf("partial deployment first observed at=%s model=%s record=%s", time.Now().UTC().Format(time.RFC3339Nano), modelID, raw)
	for _, diagnostic := range []struct {
		method  string
		request opensearch.Request
	}{
		{http.MethodPost, clusterModelTasksSearchRequest{}},
		{http.MethodGet, clusterManagerStateRequest{}},
		{http.MethodGet, clusterAutoRedeploySettingsRequest{}},
	} {
		var result json.RawMessage
		diagnosticResponse, requestErr := opensearch.Do(ctx, fixture.Client.Client, diagnostic.method, diagnostic.request, &result)
		if requestErr != nil {
			t.Logf("partial deployment diagnostic request=%T error=%v", diagnostic.request, requestErr)
			continue
		}
		if diagnosticResponse == nil {
			t.Logf("partial deployment diagnostic request=%T returned no response", diagnostic.request)
			continue
		}
		if diagnosticResponse.IsError() {
			t.Logf("partial deployment diagnostic request=%T status=%d error=%v", diagnostic.request, diagnosticResponse.StatusCode, opensearch.ParseError(diagnosticResponse))
			continue
		}
		if _, settingsRequest := diagnostic.request.(clusterAutoRedeploySettingsRequest); settingsRequest {
			var scopes map[string]map[string]json.RawMessage
			if err := json.Unmarshal(result, &scopes); err != nil {
				t.Logf("partial deployment settings decode error=%v", err)
				continue
			}
			selected := make(map[string]map[string]json.RawMessage)
			for scope, settings := range scopes {
				selected[scope] = make(map[string]json.RawMessage)
				for name, value := range settings {
					if strings.Contains(name, "model_auto_redeploy") || strings.Contains(name, "jvm_heap_memory_threshold") || strings.Contains(name, "native_memory_threshold") {
						selected[scope][name] = value
					}
				}
			}
			result, err = json.Marshal(selected)
			if err != nil {
				t.Logf("partial deployment settings encode error=%v", err)
				continue
			}
		}
		t.Logf("partial deployment diagnostic at=%s request=%T status=%d response=%s", time.Now().UTC().Format(time.RFC3339Nano), diagnostic.request, diagnosticResponse.StatusCode, result)
	}
	for _, member := range cluster.Members() {
		logs, logErr := cluster.MemberLogTail(ctx, member)
		t.Logf("partial deployment member logs member=%s at=%s error=%v\n%s", member, time.Now().UTC().Format(time.RFC3339Nano), logErr, logs)
	}
	captureSearchFailure(t, fixture, modelID, cluster.Members()...)
}
