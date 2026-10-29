package integration_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// heldPredictQuery is the query text and the matching text of the one node
	// in the test.
	heldPredictQuery = "amber lantern"
	// heldPredictSlices bounds indexing work at ten worker slices for the one
	// node plus one hundred fixture slices.
	heldPredictSlices = 10 + 100
	// predictHold delays the first model predict request past the production
	// per-attempt timeout of 10 s and within the production query deadline
	// of 20 s.
	predictHold = 12 * time.Second
)

// predictResult decodes the inference results from a model predict response.
type predictResult struct {
	InferenceResults []predictInference `json:"inference_results"`
}

type predictInference struct {
	Output []predictOutput `json:"output"`
}

type predictOutput struct {
	DataAsMap predictData `json:"dataAsMap"`
}

type predictData struct {
	Response []map[string]float64 `json:"response"`
}

// requireOnePrediction requires exactly one predict request at the proxy and
// exactly one answered exchange, and returns its token-weight map. It fails
// the test unless the response contains exactly one inference result, one
// output, and one map.
func requireOnePrediction(t *testing.T, exchange inferenceExchange) map[string]float64 {
	t.Helper()
	if exchange.Arrivals != 1 || len(exchange.Predicts) != 1 {
		t.Fatalf("the session sent %d model predict requests and the engine answered %d, want 1 of each", exchange.Arrivals, len(exchange.Predicts))
	}
	var result predictResult
	if err := json.Unmarshal(exchange.Predicts[0].Response, &result); err != nil {
		t.Fatalf("decode model predict response: %v", err)
	}
	if len(result.InferenceResults) != 1 || len(result.InferenceResults[0].Output) != 1 ||
		len(result.InferenceResults[0].Output[0].DataAsMap.Response) != 1 {
		t.Fatalf("model predict response does not contain exactly one token-weight map: %s", exchange.Predicts[0].Response)
	}
	return result.InferenceResults[0].Output[0].DataAsMap.Response[0]
}

// holdNextPredict makes the proxy wait for duration before it forwards the
// next model predict request.
func (capture *inferenceCapture) holdNextPredict(duration time.Duration) {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.hold = duration
}

// admitPredict counts one arriving model predict request and waits for a
// pending hold to end. It returns false when the client cancels the request
// during the hold.
func (capture *inferenceCapture) admitPredict(ctx context.Context) bool {
	capture.mutex.Lock()
	capture.arrivals++
	hold := capture.hold
	capture.hold = 0
	capture.mutex.Unlock()
	if hold == 0 {
		return true
	}
	timer := time.NewTimer(hold)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// TestSearchQueryPredictSentOnce sets the production defaults of 10 s per
// attempt, 2 retries, and a 20 s query deadline. The proxy delays the first
// model predict request for 12 s and then forwards it. The search must return
// the matching node after at least 12 s, and the proxy must receive exactly
// one model predict request.
func TestSearchQueryPredictSentOnce(t *testing.T) {
	t.Setenv("OPENSEARCH_REQUEST_TIMEOUT", "10s")
	t.Setenv("OPENSEARCH_MAX_RETRIES", "2")
	t.Setenv("OPENSEARCH_QUERY_DEADLINE", "20s")
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	nodeID := integration.PutOpaqueNode(t, fixture, kind, entryID, "Held predict node", heldPredictQuery, "excluded")
	integration.DrainSearchWork(t, fixture.Worker, heldPredictSlices)
	harness, capture := newInferenceCaptureHarness(t, fixture)
	capture.take()
	capture.holdNextPredict(predictHold)
	started := clock.Now()
	page, err := integration.TrySearch(harness, heldPredictQuery, "")
	elapsed := clock.Since(started)
	exchange := capture.take()
	t.Logf("held predict search returned after %s with %d predict requests, %d answered, err %v", elapsed, exchange.Arrivals, len(exchange.Predicts), err)
	if err != nil {
		t.Fatalf("search %q with a held predict: %v", heldPredictQuery, err)
	}
	requireOnePrediction(t, exchange)
	if elapsed < predictHold {
		t.Fatalf("search %q returned after %s, before the %s predict hold ended", heldPredictQuery, elapsed, predictHold)
	}
	if !slices.Contains(page.IDs, nodeID) {
		t.Fatalf("search %q returned nodes %v, want node %s", heldPredictQuery, page.IDs, nodeID)
	}
}
