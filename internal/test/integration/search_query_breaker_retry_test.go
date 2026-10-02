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
	"goodkind.io/tack/internal/clock"
)

const (
	// searchUnavailableResponse is the public text of an unavailable engine.
	searchUnavailableResponse = "Search is temporarily unavailable."
	// breakerResetDelay is the time after the search starts when the
	// recovery case restores the default threshold, inside the 1 s first wait.
	breakerResetDelay = 500 * time.Millisecond
)

// clusterSettingsRequest writes persistent cluster settings.
type clusterSettingsRequest struct{ body []byte }

func (request clusterSettingsRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_cluster/settings", bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("build cluster settings request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

// TestSearchQueryBreakerRetry opens the ML Commons memory circuit breaker in
// the disposable engine by setting its heap threshold to 1 percent. A public
// search then receives three breaker rejections and returns the unavailable
// text. A second search, with the default threshold restored 500 ms into its
// first wait, succeeds on its retry. The node stat
// ml_circuit_breaker_trigger_count counts each rejection; the model profile
// counts only the last plugins.ml_commons.monitoring_request_count predicts
// and does not increase after that window is full.
func TestSearchQueryBreakerRetry(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	info, err := fixture.Adapter.IndexInfo(t.Context(), fixture.Index)
	if err != nil {
		t.Fatalf("read serving model: %v", err)
	}
	t.Cleanup(func() {
		if err := setBreakerThreshold(context.Background(), fixture, "null"); err != nil {
			t.Errorf("restore the breaker threshold: %v", err)
		}
	})

	requireBreakerThreshold(t, fixture, "1")
	before, profileBefore := breakerTriggers(t, fixture), predictRequests(t, fixture, info.ModelID)
	started := clock.Now()
	_, err = trySearch(fixture.Harness, "breaker exhausted", "")
	elapsed := clock.Since(started)
	if err == nil || err.Error() != searchUnavailableResponse {
		t.Fatalf("search with the breaker open returned %v, want %q", err, searchUnavailableResponse)
	}
	after, profileAfter := breakerTriggers(t, fixture), predictRequests(t, fixture, info.ModelID)
	t.Logf("exhausted case: breaker triggers %d to %d, profile predicts %d to %d, elapsed %s", before, after, profileBefore, profileAfter, elapsed)
	if after-before != 3 || elapsed < 3*time.Second {
		t.Fatalf("search with the breaker open had %d breaker rejections in %s, want 3 in at least 3s", after-before, elapsed)
	}

	before, profileBefore = breakerTriggers(t, fixture), predictRequests(t, fixture, info.ModelID)
	reset := make(chan error, 1)
	started = clock.Now()
	go func() {
		time.Sleep(breakerResetDelay)
		err := setBreakerThreshold(context.Background(), fixture, "null")
		t.Logf("breaker threshold restored at=%s err=%v", clock.Now().UTC().Format(time.RFC3339Nano), err)
		reset <- err
	}()
	_, err = trySearch(fixture.Harness, "breaker recovered", "")
	elapsed = clock.Since(started)
	if resetErr := <-reset; resetErr != nil {
		t.Fatalf("restore the breaker threshold: %v", resetErr)
	}
	t.Logf("recovery search started=%s returned after %s err=%v", started.UTC().Format(time.RFC3339Nano), elapsed, err)
	if err != nil {
		t.Fatalf("search after the breaker closed: %v", err)
	}
	after, profileAfter = breakerTriggers(t, fixture), predictRequests(t, fixture, info.ModelID)
	t.Logf("recovery case: breaker triggers %d to %d, profile predicts %d to %d", before, after, profileBefore, profileAfter)
	if after-before != 1 || elapsed < time.Second {
		t.Fatalf("recovered search had %d breaker rejections in %s, want 1 in at least 1s", after-before, elapsed)
	}
}

// breakerTriggers returns the ML Commons node stat
// ml_circuit_breaker_trigger_count summed over every node. ML Commons
// increments it once for each rejected request.
func breakerTriggers(t *testing.T, fixture queryFixture) int64 {
	t.Helper()
	var stats struct {
		Nodes map[string]struct {
			Triggers *int64 `json:"ml_circuit_breaker_trigger_count"`
		} `json:"nodes"`
	}
	var raw json.RawMessage
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodGet, mlStatsRequest{}, &raw)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("read ML Commons stats: response %v err %v", response, err)
	}
	if err := json.Unmarshal(raw, &stats); err != nil || len(stats.Nodes) == 0 {
		t.Fatalf("decode ML Commons stats %s: %v", raw, err)
	}
	var total int64
	for node, values := range stats.Nodes {
		if values.Triggers == nil {
			t.Fatalf("ML Commons stats for node %s have no ml_circuit_breaker_trigger_count: %s", node, raw)
		}
		total += *values.Triggers
	}
	return total
}

// mlStatsRequest reads the ML Commons node and cluster stats.
type mlStatsRequest struct{}

func (mlStatsRequest) GetRequest(method string) (*http.Request, error) {
	return http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/stats", nil)
}

func requireBreakerThreshold(t *testing.T, fixture queryFixture, value string) {
	t.Helper()
	if err := setBreakerThreshold(t.Context(), fixture, value); err != nil {
		t.Fatalf("set the breaker threshold to %s: %v", value, err)
	}
}

// setBreakerThreshold writes the ML Commons heap threshold as a persistent
// cluster setting. The value null restores the default.
func setBreakerThreshold(ctx context.Context, fixture queryFixture, value string) error {
	body := fmt.Appendf(nil, `{"persistent":{"plugins.ml_commons.jvm_heap_memory_threshold":%s}}`, value)
	var result json.RawMessage
	response, err := opensearch.Do(ctx, fixture.Client.Client, http.MethodPut, clusterSettingsRequest{body: body}, &result)
	if err != nil {
		return err
	}
	if response == nil || response.IsError() {
		return fmt.Errorf("cluster settings response %v body %s", response, result)
	}
	return nil
}

// predictRequests returns the model profile predict count of modelID. The
// count includes at most the last monitoring_request_count predicts; the test
// logs it and asserts on breakerTriggers.
func predictRequests(t *testing.T, fixture queryFixture, modelID string) int {
	t.Helper()
	var profile struct {
		Nodes map[string]struct {
			Models map[string]struct {
				Stats struct {
					Count int `json:"count"`
				} `json:"predict_request_stats"`
			} `json:"models"`
		} `json:"nodes"`
	}
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodGet, clusterModelProfileRequest{modelID: modelID}, &profile)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("read model profile %s: response %v err %v", modelID, response, err)
	}
	total := 0
	for _, node := range profile.Nodes {
		total += node.Models[modelID].Stats.Count
	}
	return total
}
