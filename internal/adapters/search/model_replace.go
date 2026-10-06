package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/telemetry"
)

type modelUndeployRequest struct{ modelID string }

func (request modelUndeployRequest) GetRequest(method string) (*http.Request, error) {
	return modelPathRequest(method, request.modelID, "/_undeploy", "build model undeploy request")
}

type modelDeleteRequest struct{ modelID string }

func (request modelDeleteRequest) GetRequest(method string) (*http.Request, error) {
	return modelPathRequest(method, request.modelID, "", "build model delete request")
}

func modelPathRequest(method, modelID, suffix, operation string) (*http.Request, error) {
	if modelID == "" {
		return nil, fmt.Errorf("ML model ID is empty")
	}
	result, err := http.NewRequestWithContext(context.Background(), method, "/_plugins/_ml/models/"+url.PathEscape(modelID)+suffix, nil)
	if err != nil {
		return nil, modelRequestBuildError{operation: operation, err: err}
	}
	return result, nil
}

// ReplacePinnedModel undeploys and deletes the registered pinned model, then
// registers, deploys, and verifies a new copy. It returns the deleted model
// ID, empty when no copy was registered, and the new model. The new model
// has a new ID. An index that maps the deleted ID needs a full replacement.
func (a *Adapter) ReplacePinnedModel(ctx context.Context) (string, ModelInfo, error) {
	previous, err := a.findPinnedModel(ctx)
	if err == nil && previous != "" {
		err = a.removeModel(ctx, previous)
	}
	if err != nil {
		wrapped := fmt.Errorf("replace pinned OpenSearch model: %w", err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.replace_failed", slog.String("err", wrapped.Error()))
		}
		return previous, ModelInfo{}, wrapped
	}
	model, err := a.Provision(ctx)
	if err != nil {
		return previous, ModelInfo{}, err
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model.replaced", slog.String("previous_model_id", previous), slog.String("model_id", model.ID))
	return previous, model, nil
}

func (a *Adapter) removeModel(ctx context.Context, modelID string) error {
	var body json.RawMessage
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, modelUndeployRequest{modelID: modelID}, &body)
	if err := checkMLResponse(ctx, response, err); err != nil {
		return modelRemoveFailure(ctx, "undeploy", modelID, err)
	}
	response, err = opensearch.Do(ctx, a.client, http.MethodDelete, modelDeleteRequest{modelID: modelID}, &body)
	if err := checkMLResponse(ctx, response, err); err != nil {
		return modelRemoveFailure(ctx, "delete", modelID, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.model.deleted", slog.String("model_id", modelID))
	return nil
}

func modelRemoveFailure(ctx context.Context, operation, modelID string, err error) error {
	wrapped := fmt.Errorf("%s OpenSearch model %s: %w", operation, modelID, err)
	if !isLoggedModelError(err) {
		telemetry.L(ctx).ErrorContext(ctx, "search.model.remove_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
	}
	return loggedModelError{err: wrapped}
}
