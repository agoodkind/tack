package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	staleRecordInterval = 100 * time.Millisecond
	deployingRecord     = "DEPLOYING"
	modelIndexSettings  = "/.plugins-ml-model/_settings"
	modelWriteBlockOn   = `{"index":{"blocks":{"write":true}}}`
	modelWriteBlockOff  = `{"index":{"blocks":{"write":null}}}`
	modelRecordCleanup  = time.Minute
)

// startStaleRecord waits for the deploy to write DEPLOYING, writes DEPLOY_FAILED,
// and blocks writes to the model index. It releases the block and writes DEPLOYED
// when a deploy task of modelID has been COMPLETED for lag. It removes the block
// before the test's redeploy cleanup runs.
func startStaleRecord(t *testing.T, fixture integration.QueryFixture, modelID string, lag time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	since := clock.Now().UTC()
	done := make(chan error, 1)
	go func() { done <- holdStaleRecord(ctx, fixture, modelID, since, lag) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("hold stale model record: %v", err)
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), modelRecordCleanup)
		defer stop()
		if _, err := modelRecordCall(cleanup, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOff); err != nil {
			t.Errorf("remove the model index write block: %v", err)
		}
	})
}

func holdStaleRecord(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time, lag time.Duration) error {
	if err := waitForDeployingRecord(ctx, fixture, modelID); err != nil {
		return ignoreCanceled(ctx, err)
	}
	if err := writeModelState(ctx, fixture, modelID, staleRecordState); err != nil {
		return ignoreCanceled(ctx, err)
	}
	if _, err := modelRecordCall(ctx, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOn); err != nil {
		return ignoreCanceled(ctx, err)
	}
	var completed time.Time
	for ctx.Err() == nil {
		if completed.IsZero() {
			finished, err := deployTaskCompleted(ctx, fixture, modelID, since)
			if err != nil {
				return ignoreCanceled(ctx, err)
			}
			if finished {
				completed = clock.Now()
			}
		}
		if !completed.IsZero() && clock.Now().Sub(completed) >= lag {
			if _, err := modelRecordCall(ctx, fixture, http.MethodPut, modelIndexSettings, modelWriteBlockOff); err != nil {
				return ignoreCanceled(ctx, err)
			}
			return ignoreCanceled(ctx, writeModelState(ctx, fixture, modelID, deployedRecordState))
		}
		time.Sleep(staleRecordInterval)
	}
	return nil
}

func waitForDeployingRecord(ctx context.Context, fixture integration.QueryFixture, modelID string) error {
	path := "/_plugins/_ml/models/" + url.PathEscape(modelID)
	for ctx.Err() == nil {
		raw, err := modelRecordCall(ctx, fixture, http.MethodGet, path, "")
		if err != nil {
			return err
		}
		var record struct {
			State string `json:"model_state"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("decode model %s: %w", modelID, err)
		}
		if record.State == deployingRecord {
			return nil
		}
		time.Sleep(staleRecordInterval)
	}
	return ctx.Err()
}

// ignoreCanceled drops the error of a request that the end of the test canceled.
func ignoreCanceled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// modelRecordRequest is one JSON request to the engine.
type modelRecordRequest struct {
	path string
	body string
}

func (request modelRecordRequest) GetRequest(method string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(context.Background(), method, request.path, bytes.NewReader([]byte(request.body)))
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", request.path, err)
	}
	result.Header.Set("Content-Type", "application/json")
	return result, nil
}

// modelRecordCall sends one request and repeats it while the engine answers with
// status 429, which the heap use of a model deploy can cause.
func modelRecordCall(ctx context.Context, fixture integration.QueryFixture, method, path, body string) (json.RawMessage, error) {
	for {
		var raw json.RawMessage
		response, err := opensearch.Execute(ctx, fixture.Client.Client, method, modelRecordRequest{path: path, body: body}, &raw)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		if response.StatusCode == http.StatusTooManyRequests && ctx.Err() == nil {
			time.Sleep(staleRecordInterval)
			continue
		}
		if response.IsError() {
			return nil, fmt.Errorf("%s %s: %w", method, path, opensearch.ParseError(response))
		}
		return raw, nil
	}
}

func writeModelState(ctx context.Context, fixture integration.QueryFixture, modelID, state string) error {
	path := "/.plugins-ml-model/_update/" + url.PathEscape(modelID) + "?retry_on_conflict=10"
	_, err := modelRecordCall(ctx, fixture, http.MethodPost, path, fmt.Sprintf(`{"doc":{"model_state":%q}}`, state))
	return err
}

func deployTaskCompleted(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time) (bool, error) {
	body := fmt.Sprintf(`{"size":0,"track_total_hits":true,"query":{"bool":{"filter":[`+
		`{"term":{"model_id":%q}},{"term":{"task_type":"DEPLOY_MODEL"}},{"term":{"state":"COMPLETED"}},`+
		`{"range":{"create_time":{"gte":%d}}}]}}}`, modelID, since.UnixMilli())
	raw, err := modelRecordCall(ctx, fixture, http.MethodPost, "/_plugins/_ml/tasks/_search", body)
	if err != nil {
		return false, err
	}
	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("decode deploy tasks of model %s: %w", modelID, err)
	}
	return result.Hits.Total.Value > 0, nil
}
