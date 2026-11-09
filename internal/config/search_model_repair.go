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

// SearchModelRepairSettings bounds repair of ML Commons models in
// PARTIALLY_DEPLOYED or DEPLOY_FAILED state. Enabled defaults to false.
// Deployment changes must enable repair through the environment's Configs
// entry. The loader reads environment values or defaults and rejects zero or
// negative bounds.
type SearchModelRepairSettings struct {
	Enabled        bool          `env:"OPENSEARCH_MODEL_REPAIR_ENABLED"         envDefault:"false"`
	CheckInterval  time.Duration `env:"OPENSEARCH_MODEL_REPAIR_INTERVAL"        envDefault:"15s"`
	StuckAfter     time.Duration `env:"OPENSEARCH_MODEL_REPAIR_STUCK_AFTER"     envDefault:"30s"`
	AttemptSpacing time.Duration `env:"OPENSEARCH_MODEL_REPAIR_ATTEMPT_SPACING" envDefault:"45s"`
	MaxAttempts    int           `env:"OPENSEARCH_MODEL_REPAIR_MAX_ATTEMPTS"    envDefault:"3"`
}

// LoadSearchModelRepairSettings parses the model repair environment settings.
//
// LoadSearchModelRepairSettings rejects nonpositive repair intervals, stuck
// ages, attempt spacing, and attempt limits, even when repair is disabled.
//
// The loader returns these outcomes:
//
//   - Valid settings include the declared defaults for unset variables.
//   - Parsing or validation failures return an empty settings value and an
//     error.
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
