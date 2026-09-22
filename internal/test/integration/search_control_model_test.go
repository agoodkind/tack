package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// The mismatch model is a real pretrained ML Commons model that differs from
// the pin. Its small tokenizer bundle keeps registration fast.
const (
	mismatchModelName    = "amazon/neural-sparse/opensearch-neural-sparse-tokenizer-v1"
	mismatchModelVersion = "1.0.1"
)

// registerMismatchModel registers the mismatch model, waits for the ML task to
// complete, and deletes the model when the test ends.
func registerMismatchModel(t *testing.T, client *opensearchapi.Client) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"version":%q,"model_format":"TORCH_SCRIPT"}`, mismatchModelName, mismatchModelVersion)
	var started struct {
		TaskID string `json:"task_id"`
	}
	registered := requireNativeML(t, client, http.MethodPost, nativeMLRequest{path: "/_plugins/_ml/models/_register", body: []byte(body)})
	if err := json.Unmarshal(registered, &started); err != nil || started.TaskID == "" {
		t.Fatalf("register mismatch model returned no task: %s, error=%v", registered, err)
	}
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	for {
		var task struct {
			ModelID string `json:"model_id"`
			State   string `json:"state"`
			Error   string `json:"error"`
		}
		taskBody := requireNativeML(t, client, http.MethodGet, nativeMLRequest{path: "/_plugins/_ml/tasks/" + url.PathEscape(started.TaskID), body: nil})
		if err := json.Unmarshal(taskBody, &task); err != nil {
			t.Fatal(err)
		}
		if task.State == "COMPLETED" && task.ModelID != "" {
			t.Cleanup(func() { deleteMismatchModel(t, client, task.ModelID) })
			return task.ModelID
		}
		if task.State != "CREATED" && task.State != "RUNNING" {
			t.Fatalf("mismatch model registration ended in state %q: %s", task.State, task.Error)
		}
		poll := time.NewTimer(time.Second)
		select {
		case <-deadline.C:
			poll.Stop()
			t.Fatalf("mismatch model registration task %s is still %s", started.TaskID, task.State)
		case <-poll.C:
		}
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
