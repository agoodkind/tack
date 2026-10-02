package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/telemetry"
)

// verifySearchModelLocal requires the serving model to run inside the engine.
// It fails when the model is not a local sparse-encoding model or when ML
// Commons lists any connector, and logs the model ID and bundle SHA-256.
func verifySearchModelLocal(ctx context.Context, adapter *search.Adapter, modelID string) error {
	logger := telemetry.L(ctx)
	model, err := adapter.LocalModel(ctx, modelID)
	if err != nil {
		wrapped := fmt.Errorf("verify local search model %s: %w", modelID, err)
		logger.ErrorContext(ctx, "search.verify.model_local_failed", slog.String("err", wrapped.Error()),
			slog.String("model_id", modelID), slog.String("bundle_sha256", model.ContentHash))
		return wrapped
	}
	logger.InfoContext(ctx, "search.verify.model_local", slog.String("model_id", model.ModelID),
		slog.String("bundle_sha256", model.ContentHash), slog.String("function", model.Function))
	return nil
}
