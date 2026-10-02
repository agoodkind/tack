package integration

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
)

// deployTask is one DEPLOY_MODEL record from the ML Commons task index.
type deployTask struct {
	ID      string
	State   string
	Error   string
	Created time.Time
}

// ended reports whether ML Commons finished the task.
func (task deployTask) ended() bool {
	return task.State == "COMPLETED" || task.State == "COMPLETED_WITH_ERROR" || task.State == "FAILED"
}

type mlJSONRequest struct {
	path string
	body []byte
}

func (request mlJSONRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, request.path, bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("build ML request %s: %w", request.path, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

// deployTasksFrom returns every DEPLOY_MODEL task of modelID created at or
// after since, oldest first.
func deployTasksFrom(t *testing.T, fixture queryFixture, modelID string, since time.Time) []deployTask {
	t.Helper()
	body := fmt.Appendf(nil, `{"size":100,"sort":[{"create_time":{"order":"asc"}}],"query":{"bool":{"filter":[`+
		`{"term":{"model_id":%q}},{"term":{"task_type":"DEPLOY_MODEL"}},{"range":{"create_time":{"gte":%d}}}]}}}`,
		modelID, since.UnixMilli())
	var result struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					State   string `json:"state"`
					Error   string `json:"error"`
					Created int64  `json:"create_time"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	request := mlJSONRequest{path: "/_plugins/_ml/tasks/_search", body: body}
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodPost, request, &result)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("read deploy tasks of model %s: response %v err %v", modelID, response, err)
	}
	tasks := make([]deployTask, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		tasks = append(tasks, deployTask{
			ID: hit.ID, State: hit.Source.State, Error: hit.Source.Error, Created: time.UnixMilli(hit.Source.Created).UTC(),
		})
	}
	return tasks
}

// injectDeployPair sends two native deploys of modelID with no node IDs at
// the same moment on separate goroutines and returns both task IDs.
func injectDeployPair(t *testing.T, fixture queryFixture, modelID string) []string {
	t.Helper()
	release := make(chan struct{})
	ids := make([]string, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for position := range ids {
		wait.Go(func() {
			<-release
			var started struct {
				TaskID string `json:"task_id"`
			}
			request := mlJSONRequest{path: "/_plugins/_ml/models/" + url.PathEscape(modelID) + "/_deploy", body: nil}
			response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodPost, request, &started)
			if err == nil && (response == nil || response.IsError()) {
				err = fmt.Errorf("deploy response %v", response)
			}
			ids[position], errs[position] = started.TaskID, err
		})
	}
	close(release)
	wait.Wait()
	for position, err := range errs {
		if err != nil || ids[position] == "" {
			t.Fatalf("inject deploy %d of model %s: task %q err %v", position, modelID, ids[position], err)
		}
	}
	return ids
}

// repairTaskLog records the task_id of every search.model_repair.deploy_started
// record and passes every record to the previous default handler. The repair
// loop context stores no logger, and telemetry.L returns slog.Default.
type repairTaskLog struct {
	inner slog.Handler
	mu    *sync.Mutex
	ids   *[]string
}

// recordRepairTasks installs the recording handler as slog.Default for the
// test and returns a function that lists the recorded repair task IDs.
func recordRepairTasks(t *testing.T) func() []string {
	t.Helper()
	previous := slog.Default()
	recorder := repairTaskLog{inner: previous.Handler(), mu: &sync.Mutex{}, ids: &[]string{}}
	slog.SetDefault(slog.New(recorder))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []string {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		return slices.Clone(*recorder.ids)
	}
}

func (h repairTaskLog) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h repairTaskLog) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "search.model_repair.deploy_started" {
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "task_id" {
				h.mu.Lock()
				*h.ids = append(*h.ids, attribute.Value.String())
				h.mu.Unlock()
			}
			return true
		})
	}
	return h.inner.Handle(ctx, record)
}

func (h repairTaskLog) WithAttrs(attributes []slog.Attr) slog.Handler {
	return repairTaskLog{inner: h.inner.WithAttrs(attributes), mu: h.mu, ids: h.ids}
}

func (h repairTaskLog) WithGroup(name string) slog.Handler {
	return repairTaskLog{inner: h.inner.WithGroup(name), mu: h.mu, ids: h.ids}
}

// clusterManagerMember returns the container name of the current cluster
// manager. OpenSearch names each node after its container.
func clusterManagerMember(t *testing.T, fixture queryFixture) string {
	t.Helper()
	var state struct {
		Manager string `json:"cluster_manager_node"`
		Nodes   map[string]struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	response, err := opensearch.Do(t.Context(), fixture.Client.Client, http.MethodGet, clusterManagerStateRequest{}, &state)
	if err != nil || response == nil || response.IsError() || state.Nodes[state.Manager].Name == "" {
		t.Fatalf("read the cluster manager: response %v err %v state %+v", response, err, state)
	}
	return state.Nodes[state.Manager].Name
}
