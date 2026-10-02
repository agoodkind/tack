package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
	"goodkind.io/tack/internal/telemetry"
)

// SearchModelRepairSettings bound the repair of a model that ML Commons
// leaves PARTIALLY_DEPLOYED or DEPLOY_FAILED. [Config.SearchModelRepairEnabled]
// turns the repair on. The loader reads each bound from the environment or
// its default and rejects a zero or negative value.
type SearchModelRepairSettings struct {
	CheckInterval  time.Duration `env:"OPENSEARCH_MODEL_REPAIR_INTERVAL"        envDefault:"15s"`
	StuckAfter     time.Duration `env:"OPENSEARCH_MODEL_REPAIR_STUCK_AFTER"     envDefault:"30s"`
	AttemptSpacing time.Duration `env:"OPENSEARCH_MODEL_REPAIR_ATTEMPT_SPACING" envDefault:"45s"`
	MaxAttempts    int           `env:"OPENSEARCH_MODEL_REPAIR_MAX_ATTEMPTS"    envDefault:"3"`
}

// LoadSearchModelRepairSettings parses and validates the repair settings.
func LoadSearchModelRepairSettings(ctx context.Context) (SearchModelRepairSettings, error) {
	var settings SearchModelRepairSettings
	if err := env.Parse(&settings); err != nil {
		wrapped := fmt.Errorf("parse search model repair settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.model_repair.config_failed", slog.String("err", wrapped.Error()))
		return SearchModelRepairSettings{}, wrapped
	}
	if settings.CheckInterval <= 0 || settings.StuckAfter <= 0 || settings.AttemptSpacing <= 0 || settings.MaxAttempts <= 0 {
		wrapped := errors.New("search model repair interval, stuck age, attempt spacing, and attempt limit must be positive")
		telemetry.L(ctx).ErrorContext(ctx, "search.model_repair.config_failed", slog.String("err", wrapped.Error()))
		return SearchModelRepairSettings{}, wrapped
	}
	return settings, nil
}
