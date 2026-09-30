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

type clusterModelTasksSearchRequest struct{}

func (clusterModelTasksSearchRequest) GetRequest(method string) (*http.Request, error) {
	const body = `{"size":20,"sort":[{"create_time":{"order":"desc"}}]}`
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/tasks/_search", bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

type clusterManagerStateRequest struct{}

func (clusterManagerStateRequest) GetRequest(method string) (*http.Request, error) {
	return http.NewRequestWithContext(context.Background(), method, "/_cluster/state/cluster_manager_node,nodes", nil)
}

type clusterAutoRedeploySettingsRequest struct{}

func (clusterAutoRedeploySettingsRequest) GetRequest(method string) (*http.Request, error) {
	query := url.Values{
		"include_defaults": {"true"},
		"flat_settings":    {"true"},
	}
	return http.NewRequestWithContext(context.Background(), method, "/_cluster/settings?"+query.Encode(), nil)
}

func (request clusterModelPredictRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/_predict/sparse_encoding/"+url.PathEscape(request.modelID), bytes.NewReader(request.body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

type clusterMLBreakerSettingsRequest struct{}

func (clusterMLBreakerSettingsRequest) GetRequest(method string) (*http.Request, error) {
	const threshold = "plugins.ml_commons.jvm_heap_memory_threshold"
	query := url.Values{
		"include_defaults": {"true"},
		"flat_settings":    {"true"},
		"filter_path":      {"defaults." + threshold + ",persistent." + threshold + ",transient." + threshold},
	}
	return http.NewRequestWithContext(context.Background(), method, "/_cluster/settings?"+query.Encode(), nil)
}

type clusterMLBreakerNodeSettingsRequest struct{}

func (clusterMLBreakerNodeSettingsRequest) GetRequest(method string) (*http.Request, error) {
	query := url.Values{
		"flat_settings": {"true"},
		"filter_path":   {"nodes.*.name,nodes.*.settings.plugins.ml_commons.jvm_heap_memory_threshold"},
	}
	return http.NewRequestWithContext(context.Background(), method, "/_nodes/settings?"+query.Encode(), nil)
}
