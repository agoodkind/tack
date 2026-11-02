package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// staleRecordLag matches the delay between task completion and the model
	// record update in the engine log of a failed fixture.
	staleRecordLag = 6 * time.Second
	// staleRecordInterval spaces the stale writes. Each write replaces the
	// engine's own write until the test releases the record.
	staleRecordInterval = 100 * time.Millisecond
	staleRecordState    = "DEPLOY_FAILED"
	deployedRecordState = "DEPLOYED"
	// staleRecordForever keeps the stale state until the test ends.
	staleRecordForever = 24 * time.Hour
)

// TestSearchModelRecordLagAfterDeploy undeploys the pinned model and deploys it
// again on a real engine while the model record keeps DEPLOY_FAILED for
// staleRecordLag after the deploy task completes. Provision must wait and
// return the model.
func TestSearchModelRecordLagAfterDeploy(t *testing.T) {
	fixture, modelID := undeployedRecordFixture(t)
	startStaleRecordWriter(t, fixture, modelID, staleRecordLag)
	ctx := integration.WithTestOperation(t, t.Context())

	model, err := fixture.Adapter.Provision(ctx)
	if err != nil {
		t.Fatalf("provision with a late model record: %v", err)
	}
	if model.ID != modelID {
		t.Fatalf("provision returned model %s, want %s", model.ID, modelID)
	}
	deployment, err := fixture.Adapter.ModelDeployment(ctx, fixture.Index, clock.Now())
	if err != nil {
		t.Fatalf("read model deployment: %v", err)
	}
	if deployment.State != deployedRecordState {
		t.Fatalf("model state is %s after provision, want %s", deployment.State, deployedRecordState)
	}
}

// TestSearchModelRecordStaysFailedAfterDeploy keeps the record in DEPLOY_FAILED
// after the deploy task completes. Provision must return the state mismatch.
func TestSearchModelRecordStaysFailedAfterDeploy(t *testing.T) {
	fixture, modelID := undeployedRecordFixture(t)
	startStaleRecordWriter(t, fixture, modelID, staleRecordForever)

	_, err := fixture.Adapter.Provision(integration.WithTestOperation(t, t.Context()))
	if err == nil {
		t.Fatal("provision accepted a model record that stayed in DEPLOY_FAILED")
	}
	if !strings.Contains(err.Error(), staleRecordState) {
		t.Fatalf("provision error %q does not report state %s", err, staleRecordState)
	}
}

// undeployedRecordFixture returns a fixture with its pinned model undeployed.
// The fixture redeploys the model when the test ends.
func undeployedRecordFixture(t *testing.T) (integration.QueryFixture, string) {
	t.Helper()
	fixture := integration.NewQueryFixture(t)
	served, err := fixture.Adapter.ModelDeployment(integration.WithTestOperation(t, t.Context()), fixture.Index, clock.Now())
	if err != nil {
		t.Fatalf("read serving model: %v", err)
	}
	integration.UndeployNativeModel(t, fixture.Adapter, fixture.Client, served.ModelID)
	return fixture, served.ModelID
}

// startStaleRecordWriter writes DEPLOY_FAILED to the model record until a deploy
// task of modelID has been COMPLETED for lag, then writes DEPLOYED. It stops when
// the test ends and reports a rejected write as a test failure.
func startStaleRecordWriter(t *testing.T, fixture integration.QueryFixture, modelID string, lag time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	since := clock.Now().UTC()
	done := make(chan error, 1)
	go func() { done <- writeStaleRecord(ctx, fixture, modelID, since, lag) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("write stale model record: %v", err)
		}
	})
}

func writeStaleRecord(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time, lag time.Duration) error {
	var completed time.Time
	for ctx.Err() == nil {
		if err := writeModelState(ctx, fixture, modelID, staleRecordState); err != nil {
			return ignoreCanceled(ctx, err)
		}
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
			return ignoreCanceled(ctx, writeModelState(ctx, fixture, modelID, deployedRecordState))
		}
		time.Sleep(staleRecordInterval)
	}
	return nil
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
	body []byte
}

func (request modelRecordRequest) GetRequest(method string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(context.Background(), method, request.path, bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", request.path, err)
	}
	result.Header.Set("Content-Type", "application/json")
	return result, nil
}

func writeModelState(ctx context.Context, fixture integration.QueryFixture, modelID, state string) error {
	request := modelRecordRequest{
		path: "/.plugins-ml-model/_update/" + url.PathEscape(modelID),
		body: fmt.Appendf(nil, `{"doc":{"model_state":%q}}`, state),
	}
	var result json.RawMessage
	response, err := opensearch.Execute(ctx, fixture.Client.Client, http.MethodPost, request, &result)
	if err != nil {
		return fmt.Errorf("update model %s to %s: %w", modelID, state, err)
	}
	if response.IsError() {
		return fmt.Errorf("update model %s to %s: %w", modelID, state, opensearch.ParseError(response))
	}
	return nil
}

func deployTaskCompleted(ctx context.Context, fixture integration.QueryFixture, modelID string, since time.Time) (bool, error) {
	request := modelRecordRequest{
		path: "/_plugins/_ml/tasks/_search",
		body: fmt.Appendf(nil, `{"size":0,"track_total_hits":true,"query":{"bool":{"filter":[`+
			`{"term":{"model_id":%q}},{"term":{"task_type":"DEPLOY_MODEL"}},{"term":{"state":"COMPLETED"}},`+
			`{"range":{"create_time":{"gte":%d}}}]}}}`, modelID, since.UnixMilli()),
	}
	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	response, err := opensearch.Execute(ctx, fixture.Client.Client, http.MethodPost, request, &result)
	if err != nil {
		return false, fmt.Errorf("search completed deploy tasks of model %s: %w", modelID, err)
	}
	if response.IsError() {
		return false, fmt.Errorf("search completed deploy tasks of model %s: %w", modelID, opensearch.ParseError(response))
	}
	return result.Hits.Total.Value > 0, nil
}
