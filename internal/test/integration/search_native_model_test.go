package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
)

const (
	// nativePredictionWait bounds the wait for the first prediction after a
	// redeploy.
	nativePredictionWait = 5 * time.Minute
	// nativeRedeployTimeout bounds one redeploy: the deploy and the wait for
	// its first prediction.
	nativeRedeployTimeout = 10 * time.Minute
)

// nativeMLRequest is one concrete ML Commons request for the native tests.
type nativeMLRequest struct {
	path string
	body []byte
}

// GetRequest builds the request. OpenSearch rejects a body without a
// Content-Type header with status 406.
func (request nativeMLRequest) GetRequest(method string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(context.Background(), method, request.path, bytes.NewReader(request.body))
	if err != nil {
		return nil, err
	}
	if len(request.body) > 0 {
		result.Header.Set("Content-Type", "application/json")
	}
	return result, nil
}

func nativeModelPath(modelID, suffix string) string {
	return "/_plugins/_ml/models/" + url.PathEscape(modelID) + suffix
}

// requireNativeML sends one ML Commons request and decodes its response body.
func requireNativeML(t *testing.T, client *opensearchapi.Client, method string, request nativeMLRequest) json.RawMessage {
	t.Helper()
	var body json.RawMessage
	response, err := opensearch.Do(t.Context(), client.Client, method, request, &body)
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError() {
		t.Fatal(opensearch.ParseError(response))
	}
	return bytes.Clone(body)
}

func nativeModelState(t *testing.T, client *opensearchapi.Client, modelID string) string {
	t.Helper()
	var model struct {
		State string `json:"model_state"`
	}
	body := requireNativeML(t, client, http.MethodGet, nativeMLRequest{path: nativeModelPath(modelID, ""), body: nil})
	if err := json.Unmarshal(body, &model); err != nil {
		t.Fatal(err)
	}
	return model.State
}

// undeployedModelStates are the ML Commons model states that mean no node
// serves the model. The undeploy action writes UNDEPLOYED. The ML Commons
// sync-up job writes DEPLOY_FAILED when it read the model as DEPLOYED before
// the undeploy and then found no node that serves it. That write can replace
// UNDEPLOYED, and the job never changes DEPLOY_FAILED afterward.
var undeployedModelStates = []string{"UNDEPLOYED", "DEPLOY_FAILED"}

// undeployNativeModel undeploys modelID, waits until ML Commons reports that
// no node serves it, and calls [redeployNativeModel] when the test ends.
func undeployNativeModel(t *testing.T, adapter *search.Adapter, client *opensearchapi.Client, modelID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), nativeRedeployTimeout)
		defer cancel()
		if err := redeployNativeModel(ctx, adapter, client); err != nil {
			t.Errorf("redeploy native model: %v", err)
		}
	})
	requireNativeML(t, client, http.MethodPost, nativeMLRequest{path: nativeModelPath(modelID, "/_undeploy"), body: nil})
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		state := nativeModelState(t, client, modelID)
		if slices.Contains(undeployedModelStates, state) {
			return
		}
		poll := time.NewTimer(time.Second)
		select {
		case <-deadline.C:
			poll.Stop()
			t.Fatalf("native model %s state is %s after undeploy", modelID, state)
		case <-poll.C:
		}
	}
}

// redeployNativeModel deploys the pinned model and returns after the model
// serves one prediction. The ML Commons memory circuit breaker rejects a
// prediction with status 429 while JVM heap use is above its threshold, 85
// percent of the heap by default. Heap use includes unreachable objects that
// the next young collection frees. A deploy allocates the model bundle on the
// heap, and the next young collection runs only after the engine fills the
// young generation. A rejection with status 429 continues the wait. Every
// other failure returns at once.
func redeployNativeModel(ctx context.Context, adapter *search.Adapter, client *opensearchapi.Client) error {
	model, err := adapter.Provision(ctx)
	if err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, nativePredictionWait)
	defer cancel()
	request := nativeMLRequest{
		path: "/_plugins/_ml/_predict/sparse_encoding/" + url.PathEscape(model.ID),
		body: []byte(`{"text_docs":["redeployed model"]}`),
	}
	for {
		var body json.RawMessage
		response, err := opensearch.Do(deadline, client.Client, http.MethodPost, request, &body)
		if err != nil {
			return fmt.Errorf("predict with redeployed model %s: %w", model.ID, err)
		}
		if !response.IsError() {
			return nil
		}
		rejection := opensearch.ParseError(response)
		if response.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("predict with redeployed model %s: %w", model.ID, rejection)
		}
		wait := time.NewTimer(time.Second)
		select {
		case <-deadline.Done():
			wait.Stop()
			return fmt.Errorf("redeployed model %s rejected every prediction for %s: %w", model.ID, nativePredictionWait, rejection)
		case <-wait.C:
		}
	}
}
