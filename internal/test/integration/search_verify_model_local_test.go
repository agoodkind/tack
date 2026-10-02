package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
)

// TestSearchModelLocalRejectsConnector requires the deployed search model to
// pass the local model check, then registers one remote connector and
// requires the check to fail with an error that reports the connector ID.
// After the connector is deleted, the check must pass again.
func TestSearchModelLocalRejectsConnector(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	info, err := fixture.Adapter.IndexInfo(t.Context(), fixture.Index)
	if err != nil {
		t.Fatalf("read serving index %s: %v", fixture.Index, err)
	}
	requireLocalModel(t, fixture.Adapter, info.ModelID)

	connectorID := createRemoteConnector(t, fixture.Client)
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			deleteRemoteConnector(t, fixture.Client, connectorID)
		}
	})
	model, err := fixture.Adapter.LocalModel(t.Context(), info.ModelID)
	if err == nil || !strings.Contains(err.Error(), connectorID) {
		t.Fatalf("local model check with connector %s = %v, want an error that reports the connector ID", connectorID, err)
	}
	if model.ModelID != info.ModelID || model.ContentHash != search.PinnedModel.BundleDigest {
		t.Fatalf("failed local model check reported model %q with SHA-256 %q, want %q with %q", model.ModelID, model.ContentHash, info.ModelID, search.PinnedModel.BundleDigest)
	}

	deleteRemoteConnector(t, fixture.Client, connectorID)
	deleted = true
	requireLocalModel(t, fixture.Adapter, info.ModelID)
}

func requireLocalModel(t *testing.T, adapter *search.Adapter, modelID string) {
	t.Helper()
	model, err := adapter.LocalModel(t.Context(), modelID)
	if err != nil {
		t.Fatalf("local model check for %s: %v", modelID, err)
	}
	if model.ModelID != modelID || model.ContentHash != search.PinnedModel.BundleDigest || model.ConnectorTotal != 0 {
		t.Fatalf("local model check = %+v, want model %s with SHA-256 %s and no connector", model, modelID, search.PinnedModel.BundleDigest)
	}
}

type connectorCreateRequest struct{ body []byte }

func (request connectorCreateRequest) GetRequest(method string) (*http.Request, error) {
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/connectors/_create", bytes.NewReader(request.body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	return httpRequest, nil
}

type connectorDeleteRequest struct{ connectorID string }

func (request connectorDeleteRequest) GetRequest(method string) (*http.Request, error) {
	return http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/connectors/"+url.PathEscape(request.connectorID), nil)
}

type remoteConnectorAction struct {
	ActionType  string            `json:"action_type"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	RequestBody string            `json:"request_body"`
}

type remoteConnector struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Version     int                     `json:"version"`
	Protocol    string                  `json:"protocol"`
	Parameters  map[string]string       `json:"parameters"`
	Auth        map[string]string       `json:"credential"`
	Actions     []remoteConnectorAction `json:"actions"`
}

// createRemoteConnector registers an embedding connector to an endpoint on
// the ML Commons default trusted list. Registration contacts no endpoint.
func createRemoteConnector(t *testing.T, client *opensearchapi.Client) string {
	t.Helper()
	body, err := json.Marshal(remoteConnector{
		Name: "tack local model check probe", Description: "remote connector that the local model check must reject",
		Version: 1, Protocol: "http",
		Parameters: map[string]string{"model": "text-embedding-3-small"},
		Auth:       map[string]string{"probe": "unused"},
		Actions: []remoteConnectorAction{{
			ActionType: "predict", Method: http.MethodPost, URL: "https://api.openai.com/v1/embeddings",
			Headers:     map[string]string{"Authorization": "Bearer ${credential.probe}"},
			RequestBody: `{ "input": ${parameters.input}, "model": "${parameters.model}" }`,
		}},
	})
	if err != nil {
		t.Fatalf("marshal remote connector: %v", err)
	}
	var created struct {
		ConnectorID string `json:"connector_id"`
	}
	response, err := opensearch.Do(t.Context(), client.Client, http.MethodPost, connectorCreateRequest{body: body}, &created)
	if err != nil || response.IsError() || created.ConnectorID == "" {
		t.Fatalf("create remote connector: response %v, error %v", response, err)
	}
	return created.ConnectorID
}

func deleteRemoteConnector(t *testing.T, client *opensearchapi.Client, connectorID string) {
	t.Helper()
	var deleted json.RawMessage
	response, err := opensearch.Do(context.WithoutCancel(t.Context()), client.Client, http.MethodDelete, connectorDeleteRequest{connectorID: connectorID}, &deleted)
	if err != nil || response.IsError() {
		t.Errorf("delete remote connector %s: response %v, error %v", connectorID, response, err)
	}
}
