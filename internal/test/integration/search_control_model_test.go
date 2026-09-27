package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/clock"
)

// The mismatch model is a real pretrained ML Commons model that differs from
// the pinned model. The test uses it because its tokenizer bundle is small
// and registers quickly.
const (
	mismatchModelName    = "amazon/neural-sparse/opensearch-neural-sparse-tokenizer-v1"
	mismatchModelVersion = "1.0.1"
	// mismatchRegistrationWindow bounds the total time of all registration
	// attempts.
	mismatchRegistrationWindow = 5 * time.Minute
	// mismatchBreakerInterval is the delay before the test repeats a
	// registration that the ML Commons memory circuit breaker rejected.
	mismatchBreakerInterval = 5 * time.Second
	// mlBreakerOpenText is the ML Commons task error of a rejection by an
	// open memory circuit breaker.
	mlBreakerOpenText = "Circuit Breaker is open"
)

// mismatchTask is the ML Commons state of one registration task.
type mismatchTask struct {
	ModelID string `json:"model_id"`
	State   string `json:"state"`
	Error   string `json:"error"`
}

// registerMismatchModel registers the mismatch model, waits for the ML task to
// complete, and deletes the model when the test ends. A registration that the
// ML Commons memory circuit breaker rejects runs again every
// mismatchBreakerInterval until mismatchRegistrationWindow ends.
func registerMismatchModel(t *testing.T, client *opensearchapi.Client) string {
	t.Helper()
	deadline := clock.Now().Add(mismatchRegistrationWindow)
	for {
		task := runMismatchRegistration(t, client, deadline)
		if task.ModelID != "" {
			t.Cleanup(func() { deleteMismatchModel(t, client, task.ModelID) })
		}
		if task.State == "COMPLETED" && task.ModelID != "" {
			return task.ModelID
		}
		retryAt := clock.Now().Add(mismatchBreakerInterval)
		if !strings.Contains(task.Error, mlBreakerOpenText) || retryAt.After(deadline) {
			t.Fatalf("mismatch model registration ended in state %q: %s", task.State, task.Error)
		}
		waitUntil(t, retryAt)
	}
}

// runMismatchRegistration starts one registration task and polls it until it
// leaves the CREATED and RUNNING states. It fails the test at deadline.
func runMismatchRegistration(t *testing.T, client *opensearchapi.Client, deadline time.Time) mismatchTask {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"version":%q,"model_format":"TORCH_SCRIPT"}`, mismatchModelName, mismatchModelVersion)
	var started struct {
		TaskID string `json:"task_id"`
	}
	registered := requireNativeML(t, client, http.MethodPost, nativeMLRequest{path: "/_plugins/_ml/models/_register", body: []byte(body)})
	if err := json.Unmarshal(registered, &started); err != nil || started.TaskID == "" {
		t.Fatalf("register mismatch model returned no task: %s, error=%v", registered, err)
	}
	for {
		var task mismatchTask
		taskBody := requireNativeML(t, client, http.MethodGet, nativeMLRequest{path: "/_plugins/_ml/tasks/" + url.PathEscape(started.TaskID), body: nil})
		if err := json.Unmarshal(taskBody, &task); err != nil {
			t.Fatal(err)
		}
		if task.State != "CREATED" && task.State != "RUNNING" {
			return task
		}
		if clock.Now().After(deadline) {
			t.Fatalf("mismatch model registration task %s is still %s", started.TaskID, task.State)
		}
		waitUntil(t, clock.Now().Add(time.Second))
	}
}

func deleteMismatchModel(t *testing.T, client *opensearchapi.Client, modelID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	response, err := opensearch.Do[json.RawMessage](ctx, client.Client, http.MethodDelete,
		nativeMLRequest{path: nativeModelPath(modelID, ""), body: nil}, nil)
	if err != nil {
		t.Errorf("delete mismatch model %s: %v", modelID, err)
		return
	}
	if response.IsError() {
		t.Errorf("delete mismatch model %s: %v", modelID, opensearch.ParseError(response))
	}
}
