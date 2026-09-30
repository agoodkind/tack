package integration

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
)

type clusterModelRecordRequest struct {
	modelID string
}

func (request clusterModelRecordRequest) GetRequest(method string) (*http.Request, error) {
	return http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/models/"+url.PathEscape(request.modelID), nil)
}

type clusterModelProfileRequest struct {
	modelID string
}

func (request clusterModelProfileRequest) GetRequest(method string) (*http.Request, error) {
	return http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/profile/models/"+url.PathEscape(request.modelID), nil)
}

type clusterModelPredictRequest struct {
	modelID string
	body    []byte
}

func (request clusterModelPredictRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/_predict/sparse_encoding/"+url.PathEscape(request.modelID), bytes.NewReader(request.body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}
