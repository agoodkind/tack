package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"goodkind.io/tack/internal/telemetry"
)

const (
	pinnedModelFormat    = "TORCH_SCRIPT"
	pinnedModelAlgorithm = "SPARSE_ENCODING"
	deployedModelState   = "DEPLOYED"
)

func (a *Adapter) getModel(ctx context.Context, modelID string) (registeredModel, error) {
	var model registeredModel
	response, err := opensearch.Do(ctx, a.client, http.MethodGet, modelGetRequest{modelID: modelID}, &model)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("read OpenSearch model %s: %w", modelID, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.read_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return registeredModel{}, loggedModelError{err: wrapped}
	}
	return model, nil
}

// VerifyModel checks the registered model's pinned identity and live placement.
func (a *Adapter) VerifyModel(ctx context.Context, modelID string) error {
	if err := a.verifyModel(ctx, modelID); err != nil {
		wrapped := fmt.Errorf("verify OpenSearch model %s: %w", modelID, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.verify_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return wrapped
	}
	return nil
}

// verifyModel compares the registered model with the pin and checks that a
// worker node runs the deployed model. It returns one joined error that lists
// every mismatch. ML Commons stores neither the pretrained release version nor
// the tokenizer digest in the model document. The bundle SHA-256 and size
// identify the release, and [Adapter.VerifyIndex] compares the tokenizer
// digest that the index mapping records.
func (a *Adapter) verifyModel(ctx context.Context, modelID string) error {
	model, err := a.getModel(ctx, modelID)
	if err != nil {
		return err
	}
	mismatches := modelMismatches(model)
	deployed, err := a.hasDeployedWorker(ctx, modelID)
	if err != nil {
		return err
	}
	if !deployed {
		mismatches = append(mismatches, errors.New("model placement has no eligible deployed worker node"))
	}
	if len(mismatches) > 0 {
		wrapped := fmt.Errorf("OpenSearch model %s does not match the pin: %w", modelID, errors.Join(mismatches...))
		telemetry.L(ctx).ErrorContext(ctx, "search.model.pin_mismatch", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		return loggedModelError{err: wrapped}
	}
	return nil
}

func modelMismatches(model registeredModel) []error {
	var mismatches []error
	for _, value := range []struct{ name, actual, expected string }{
		{name: "name", actual: model.Name, expected: PinnedModel.Name},
		{name: "format", actual: model.ModelFormat, expected: pinnedModelFormat},
		{name: "algorithm", actual: model.Algorithm, expected: pinnedModelAlgorithm},
		{name: "bundle SHA-256", actual: model.ContentHash, expected: PinnedModel.BundleDigest},
		{name: "state", actual: model.State, expected: deployedModelState},
	} {
		if value.actual != value.expected {
			mismatches = append(mismatches, fmt.Errorf("model %s is %q, want %q", value.name, value.actual, value.expected))
		}
	}
	if model.ContentSize != PinnedModel.BundleBytes {
		mismatches = append(mismatches, fmt.Errorf("model bundle size is %d bytes, want %d", model.ContentSize, PinnedModel.BundleBytes))
	}
	return mismatches
}

func (a *Adapter) hasDeployedWorker(ctx context.Context, modelID string) (bool, error) {
	var profile modelProfile
	response, err := opensearch.Do(ctx, a.client, http.MethodGet, modelProfileRequest{modelID: modelID}, &profile)
	if err := checkMLResponse(ctx, response, err); err != nil {
		wrapped := fmt.Errorf("read OpenSearch model %s placement: %w", modelID, err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.placement_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
		}
		return false, loggedModelError{err: wrapped}
	}
	for _, node := range profile.Nodes {
		placement, exists := node.Models[modelID]
		if exists && placement.State == deployedModelState && len(placement.WorkerNodes) > 0 {
			return true, nil
		}
	}
	return false, nil
}
