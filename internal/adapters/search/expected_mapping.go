package search

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

func expectedIndexMapping(ctx context.Context, index string, spec *IndexSpec) (mappingDef, error) {
	var none mappingDef
	if err := spec.Validate(ctx); err != nil {
		wrapped := fmt.Errorf("verify OpenSearch index %s: %w", index, err)
		if !isLoggedModelError(err) {
			log := telemetry.L(ctx)
			log.ErrorContext(
				ctx,
				"search.index.verify_invalid",
				slog.String("err", wrapped.Error()),
				slog.String("index", index),
			)
		}

		return none, wrapped
	}

	expected, err := spec.Mapping(ctx)
	if err != nil {
		wrapped := fmt.Errorf("build expected OpenSearch index %s mapping: %w", index, err)
		if !isLoggedModelError(err) {
			log := telemetry.L(ctx)
			log.ErrorContext(
				ctx,
				"search.index.mapping_encode_failed",
				slog.String("err", wrapped.Error()),
				slog.String("index", index),
			)
		}

		return none, wrapped
	}

	return expected, nil
}
