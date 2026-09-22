package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
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

// undeployNativeModel undeploys modelID, waits until ML Commons reports it
// undeployed, and calls redeploy when the test ends.
func undeployNativeModel(t *testing.T, client *opensearchapi.Client, modelID string, redeploy func(context.Context) error) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Minute)
		defer cancel()
		if err := redeploy(ctx); err != nil {
			t.Errorf("redeploy native model: %v", err)
		}
	})
	requireNativeML(t, client, http.MethodPost, nativeMLRequest{path: nativeModelPath(modelID, "/_undeploy"), body: nil})
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		state := nativeModelState(t, client, modelID)
		if state == "UNDEPLOYED" {
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
