package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
)

// connectorSearchBody requests the name and protocol of at most 100
// connectors. The source filter excludes connector credentials, and the
// reported total counts every connector.
const connectorSearchBody = `{"query":{"match_all":{}},"_source":["name","protocol"],"size":100,"track_total_hits":true}`

// LocalModel is the ML Commons record of one deployed model and of every
// registered connector. A connector lets ML Commons send text to a service
// outside the engine.
type LocalModel struct {
	ModelID         string
	Function        string
	ContentHash     string
	ConnectorID     string
	InlineConnector bool
	ConnectorTotal  int
	Connectors      []Connector
}

// Connector is one registered ML Commons connector.
type Connector struct {
	ID       string
	Name     string
	Protocol string
}

// localModelError is a LocalModel failure. LocalModel logs nothing; the caller
// that owns the check logs the failure once.
type localModelError struct {
	operation string
	err       error
}

func (e localModelError) Error() string { return e.operation + ": " + e.err.Error() }
func (e localModelError) Unwrap() error { return e.err }

type localModelDocument struct {
	Algorithm   string          `json:"algorithm"`
	ConnectorID string          `json:"connector_id"`
	Connector   json.RawMessage `json:"connector"`
	ContentHash string          `json:"model_content_hash_value"`
}

type connectorSearchRequest struct{ body []byte }

func (request connectorSearchRequest) GetRequest(method string) (*http.Request, error) {
	return jsonBodyRequest(method, "/_plugins/_ml/connectors/_search", request.body, "build connector search request")
}

type connectorSearchResult struct {
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID     string `json:"_id"`
			Source struct {
				Name     string `json:"name"`
				Protocol string `json:"protocol"`
			} `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

// LocalModel reads the model document for modelID and every ML Commons
// connector. It returns an error unless the model is a local sparse-encoding
// model without a connector and ML Commons lists no connector. The returned
// record includes the model ID and bundle SHA-256 on every completed read.
func (a *Adapter) LocalModel(ctx context.Context, modelID string) (LocalModel, error) {
	model, err := a.readLocalModel(ctx, modelID)
	if err != nil {
		return LocalModel{}, err
	}
	if err := a.readConnectors(ctx, &model); err != nil {
		return model, err
	}
	if violations := localModelViolations(model); len(violations) > 0 {
		return model, localModelError{operation: "OpenSearch model " + modelID + " is not local", err: errors.Join(violations...)}
	}
	return model, nil
}

func (a *Adapter) readLocalModel(ctx context.Context, modelID string) (LocalModel, error) {
	var document localModelDocument
	response, err := opensearch.Do(ctx, a.client, http.MethodGet, modelGetRequest{modelID: modelID}, &document)
	if err := localModelResponseError(response, err); err != nil {
		return LocalModel{}, localModelError{operation: "read OpenSearch model " + modelID, err: err}
	}
	inline := len(document.Connector) > 0 && string(document.Connector) != "null"
	return LocalModel{
		ModelID: modelID, Function: document.Algorithm, ContentHash: document.ContentHash,
		ConnectorID: document.ConnectorID, InlineConnector: inline, ConnectorTotal: 0, Connectors: []Connector{},
	}, nil
}

// readConnectors adds every registered connector to model. ML Commons
// returns an empty result before the first connector creates its index.
func (a *Adapter) readConnectors(ctx context.Context, model *LocalModel) error {
	var found connectorSearchResult
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, connectorSearchRequest{body: []byte(connectorSearchBody)}, &found)
	if err := localModelResponseError(response, err); err != nil {
		return localModelError{operation: "search OpenSearch ML connectors", err: err}
	}
	model.ConnectorTotal = found.Hits.Total.Value
	for _, hit := range found.Hits.Hits {
		model.Connectors = append(model.Connectors, Connector{ID: hit.ID, Name: hit.Source.Name, Protocol: hit.Source.Protocol})
	}
	return nil
}

// localModelResponseError is checkMLResponse without its log line.
func localModelResponseError(response *opensearch.Response, err error) error {
	if err != nil {
		return err
	}
	if response == nil {
		return errors.New("OpenSearch returned no ML response")
	}
	if response.IsError() {
		return localModelError{operation: "OpenSearch ML request failed", err: opensearch.ParseError(response)}
	}
	return nil
}

func localModelViolations(model LocalModel) []error {
	var violations []error
	if model.Function != pinnedModelAlgorithm {
		violations = append(violations, fmt.Errorf("model function is %q, want %q", model.Function, pinnedModelAlgorithm))
	}
	if model.ConnectorID != "" {
		violations = append(violations, fmt.Errorf("model uses connector %s", model.ConnectorID))
	}
	if model.InlineConnector {
		violations = append(violations, errors.New("model defines an inline connector"))
	}
	if model.ConnectorTotal > 0 {
		listed := make([]string, 0, len(model.Connectors))
		for _, connector := range model.Connectors {
			listed = append(listed, fmt.Sprintf("%s (name %q, protocol %q)", connector.ID, connector.Name, connector.Protocol))
		}
		violations = append(violations, fmt.Errorf("ML Commons lists %d connectors: %s", model.ConnectorTotal, strings.Join(listed, ", ")))
	}
	return violations
}
