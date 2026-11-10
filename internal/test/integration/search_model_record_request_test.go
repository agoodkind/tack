package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"goodkind.io/tack/internal/test/integration"
)

// modelRecordRequest is one JSON request to the engine.
type modelRecordRequest struct {
	path string
	body string
}

func (request modelRecordRequest) GetRequest(method string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(
		context.Background(),
		method,
		request.path,
		bytes.NewReader([]byte(request.body)),
	)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", request.path, err)
	}

	result.Header.Set("Content-Type", "application/json")

	return result, nil
}

// modelRecordCall sends one request and repeats it while the engine answers with
// status 429 or the request exceeds its timeout. The heap use of a model deploy
// can cause both.
func modelRecordCall(
	ctx context.Context,
	fixture integration.QueryFixture,
	method string,
	path string,
	body string,
) (json.RawMessage, error) {
	for {
		var raw json.RawMessage
		response, err := opensearch.Execute(
			ctx,
			fixture.Client.Client,
			method,
			modelRecordRequest{path: path, body: body},
			&raw,
		)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			continue
		}

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

func writeModelState(
	ctx context.Context,
	fixture integration.QueryFixture,
	modelID string,
	state string,
) error {
	path := "/.plugins-ml-model/_update/" + url.PathEscape(modelID) + "?retry_on_conflict=10"
	_, err := modelRecordCall(
		ctx,
		fixture,
		http.MethodPost,
		path,
		fmt.Sprintf(`{"doc":{"model_state":%q}}`, state),
	)

	return err
}

// deployTaskCount returns the number of deploy tasks of modelID in state since a
// time.
func deployTaskCount(
	ctx context.Context,
	fixture integration.QueryFixture,
	modelID string,
	since time.Time,
	state string,
) (int, error) {
	body := fmt.Sprintf(
		`{"size":0,"track_total_hits":true,"query":{"bool":{"filter":[`+
			`{"term":{"model_id":%q}},{"term":{"task_type":"DEPLOY_MODEL"}},{"term":{"state":%q}},`+
			`{"range":{"create_time":{"gte":%d}}}]}}}`,
		modelID,
		state,
		since.UnixMilli(),
	)
	raw, err := modelRecordCall(ctx, fixture, http.MethodPost, "/_plugins/_ml/tasks/_search", body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("decode deploy tasks of model %s: %w", modelID, err)
	}

	return result.Hits.Total.Value, nil
}
