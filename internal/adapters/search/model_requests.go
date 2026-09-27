package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type modelSearchRequest struct{ body []byte }

type modelRequestBuildError struct {
	operation string
	err       error
}

func (e modelRequestBuildError) Error() string { return e.operation + ": " + e.err.Error() }
func (e modelRequestBuildError) Unwrap() error { return e.err }

// jsonBodyRequest builds one request with a JSON body. OpenSearch rejects a
// request body without a Content-Type header with status 406. opensearch.Do
// replaces the placeholder context with the caller context.
func jsonBodyRequest(method, path string, body []byte, operation string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	if err != nil {
		return nil, modelRequestBuildError{operation: operation, err: err}
	}
	result.Header.Set("Content-Type", "application/json")
	return result, nil
}

func (request modelSearchRequest) GetRequest(method string) (*http.Request, error) {
	return jsonBodyRequest(method, "/_plugins/_ml/models/_search", request.body, "build model search request")
}

type modelRegisterRequest struct{ body []byte }

func (request modelRegisterRequest) GetRequest(method string) (*http.Request, error) {
	return jsonBodyRequest(method, "/_plugins/_ml/models/_register", request.body, "build model register request")
}

type modelTaskRequest struct{ taskID string }

func (request modelTaskRequest) GetRequest(method string) (*http.Request, error) {
	if request.taskID == "" {
		return nil, fmt.Errorf("ML task ID is empty")
	}
	result, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/tasks/"+url.PathEscape(request.taskID), nil)
	if err != nil {
		return nil, modelRequestBuildError{operation: "build model task request", err: err}
	}
	return result, nil
}

type modelDeployRequest struct{ modelID string }

func (request modelDeployRequest) GetRequest(method string) (*http.Request, error) {
	if request.modelID == "" {
		return nil, fmt.Errorf("ML model ID is empty")
	}
	result, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/models/"+url.PathEscape(request.modelID)+"/_deploy", nil)
	if err != nil {
		return nil, modelRequestBuildError{operation: "build model deploy request", err: err}
	}
	return result, nil
}

type modelGetRequest struct{ modelID string }

func (request modelGetRequest) GetRequest(method string) (*http.Request, error) {
	if request.modelID == "" {
		return nil, fmt.Errorf("ML model ID is empty")
	}
	result, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/models/"+url.PathEscape(request.modelID), nil)
	if err != nil {
		return nil, modelRequestBuildError{operation: "build model get request", err: err}
	}
	return result, nil
}

type modelProfileRequest struct{ modelID string }

func (request modelProfileRequest) GetRequest(method string) (*http.Request, error) {
	if request.modelID == "" {
		return nil, fmt.Errorf("ML model ID is empty")
	}
	result, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/profile/models/"+url.PathEscape(request.modelID), nil)
	if err != nil {
		return nil, modelRequestBuildError{operation: "build model profile request", err: err}
	}
	return result, nil
}

type modelTask struct {
	ModelID string `json:"model_id"`
	State   string `json:"state"`
	Error   string `json:"error"`
}

type modelTaskState string

const (
	modelTaskCreated   modelTaskState = "CREATED"
	modelTaskRunning   modelTaskState = "RUNNING"
	modelTaskCompleted modelTaskState = "COMPLETED"
)

type modelTaskStart struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type registeredModel struct {
	Name        string `json:"name"`
	Algorithm   string `json:"algorithm"`
	ModelFormat string `json:"model_format"`
	State       string `json:"model_state"`
	ContentSize int64  `json:"model_content_size_in_bytes"`
	ContentHash string `json:"model_content_hash_value"`
}

type modelSearchResult struct {
	Hits struct {
		Hits []struct {
			ID     string          `json:"_id"`
			Source registeredModel `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

type modelProfile struct {
	Nodes map[string]struct {
		Models map[string]struct {
			State       string   `json:"model_state"`
			WorkerNodes []string `json:"worker_nodes"`
		} `json:"models"`
	} `json:"nodes"`
}

// MarshalModelSearchBody encodes the pinned model lookup. ML Commons maps
// the model name as analyzed text with an exact name.keyword subfield. The
// query excludes model chunk documents that include chunk_number.
func MarshalModelSearchBody() ([]byte, error) {
	type termQuery struct {
		Name string `json:"name.keyword"`
	}
	type existsQuery struct {
		Field string `json:"field"`
	}
	type filterClause struct {
		Term termQuery `json:"term"`
	}
	type mustNotClause struct {
		Exists existsQuery `json:"exists"`
	}
	type boolQuery struct {
		Filter  []filterClause  `json:"filter"`
		MustNot []mustNotClause `json:"must_not"`
	}
	type searchQuery struct {
		Bool boolQuery `json:"bool"`
	}
	query := searchQuery{Bool: boolQuery{
		Filter:  []filterClause{{Term: termQuery{Name: PinnedModel.Name}}},
		MustNot: []mustNotClause{{Exists: existsQuery{Field: "chunk_number"}}},
	}}
	body, err := json.Marshal(struct {
		Query searchQuery `json:"query"`
		Size  int         `json:"size"`
	}{Query: query, Size: 100})
	if err != nil {
		return nil, fmt.Errorf("marshal OpenSearch model search: %w", err)
	}
	return body, nil
}
