package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/telemetry"
)

// connectorListLimit bounds the connectors that one check lists by ID. The
// reported total counts every connector.
const connectorListLimit = 100

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
		wrapped := fmt.Errorf("OpenSearch model %s is not local: %w", modelID, errors.Join(violations...))
		telemetry.L(ctx).ErrorContext(ctx, "search.model.not_local", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		return model, wrapped
	}
	return model, nil
}

func (a *Adapter) readLocalModel(ctx context.Context, modelID string) (LocalModel, error) {
	var document localModelDocument
	response, err := opensearch.Do(ctx, a.client, http.MethodGet, modelGetRequest{modelID: modelID}, &document)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("read OpenSearch model %s: %w", modelID, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.local_read_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		return LocalModel{}, wrapped
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
	body, err := connectorSearchBody(ctx)
	if err != nil {
		return err
	}
	var found connectorSearchResult
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, connectorSearchRequest{body: body}, &found)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("search OpenSearch ML connectors: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.connector_search_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	model.ConnectorTotal = found.Hits.Total.Value
	for _, hit := range found.Hits.Hits {
		model.Connectors = append(model.Connectors, Connector{ID: hit.ID, Name: hit.Source.Name, Protocol: hit.Source.Protocol})
	}
	return nil
}

// connectorSearchBody requests the name and protocol of each connector. The
// source filter excludes connector credentials.
func connectorSearchBody(ctx context.Context) ([]byte, error) {
	type matchAllQuery struct {
		MatchAll struct{} `json:"match_all"`
	}
	body, err := json.Marshal(struct {
		Query          matchAllQuery `json:"query"`
		Source         []string      `json:"_source"`
		Size           int           `json:"size"`
		TrackTotalHits bool          `json:"track_total_hits"`
	}{Query: matchAllQuery{MatchAll: struct{}{}}, Source: []string{"name", "protocol"}, Size: connectorListLimit, TrackTotalHits: true})
	if err != nil {
		wrapped := fmt.Errorf("marshal OpenSearch connector search: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model.connector_encode_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return body, nil
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
