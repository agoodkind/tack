package search

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// ModelInfo identifies the model and artifacts accepted by the native index.
type ModelInfo struct {
	ID              string
	Name            string
	Version         string
	BundleDigest    string
	TokenizerDigest string
	Algorithm       string
	BundleBytes     int64
	RuntimeBytes    int64
}

// PinnedModel is the only model accepted by Tack's native semantic mapping.
var PinnedModel = ModelInfo{
	ID:              "",
	Name:            "amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte",
	Version:         "1.0.0",
	BundleDigest:    "08879b93faf4a92506a44e150f47bbc4" + "cadc9a2f083350c4dc79434738303047",
	TokenizerDigest: "ea725c60b9022a7a" + "491ffc348b5622a" + "199853c806d625f6" + "73d0e2ebf1c3b5312",
	Algorithm:       "sparse",
	BundleBytes:     554924400,
	RuntimeBytes:    0,
}

// Provision reuses the registered pinned pretrained model when one exists and
// registers the model when none exists. It then deploys and verifies the model.
func (a *Adapter) Provision(ctx context.Context) (ModelInfo, error) {
	modelID, err := a.findPinnedModel(ctx)
	if err == nil && modelID == "" {
		modelID, err = a.registerPinnedModel(ctx)
	}
	if err == nil {
		err = a.deployModel(ctx, modelID)
	}
	if err == nil {
		err = a.verifyModel(ctx, modelID)
	}
	if err != nil {
		wrapped := fmt.Errorf("provision pinned OpenSearch model: %w", err)
		if !isLoggedModelError(err) {
			telemetry.L(ctx).ErrorContext(ctx, "search.model.provision_failed", slog.String("err", wrapped.Error()))
		}
		return ModelInfo{}, wrapped
	}
	model := PinnedModel
	model.ID = modelID
	return model, nil
}
