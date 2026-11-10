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

// SearchRetentionSettings supplies thresholds for a full search index
// replacement from FoundationDB. Retired page records and their bytes remain in
// the serving index until replacement. The loader reads environment values or
// defaults and rejects zero or negative values.
type SearchRetentionSettings struct {
	MaxRetiredPages  int64         `env:"OPENSEARCH_MAX_RETIRED_PAGES"        envDefault:"1000000"`
	MaxRetirementAge time.Duration `env:"OPENSEARCH_MAX_RETIREMENT_AGE"       envDefault:"168h"`
	MaxIndexBytes    int64         `env:"OPENSEARCH_MAX_INDEX_BYTES"          envDefault:"8589934592"`
	CheckInterval    time.Duration `env:"OPENSEARCH_RETENTION_CHECK_INTERVAL" envDefault:"10m"`
}

// LoadSearchRetentionSettings reads the search retention thresholds.
//
// LoadSearchRetentionSettings parses environment values or defaults and rejects
// nonpositive thresholds or check intervals.
//
// The loader validates these settings:
//
//   - MaxRetiredPages bounds the retired page count.
//   - MaxRetirementAge bounds the oldest retirement age.
//   - MaxIndexBytes bounds index bytes.
//   - CheckInterval sets the interval between retention checks.
func LoadSearchRetentionSettings(ctx context.Context) (SearchRetentionSettings, error) {
	var settings SearchRetentionSettings
	if err := env.Parse(&settings); err != nil {
		wrapped := fmt.Errorf("parse search retention settings: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.retention.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchRetentionSettings{}, wrapped
	}

	if settings.MaxRetiredPages <= 0 || settings.MaxRetirementAge <= 0 ||
		settings.MaxIndexBytes <= 0 ||
		settings.CheckInterval <= 0 {
		wrapped := errors.New("search retention thresholds and check interval must be positive")
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.retention.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchRetentionSettings{}, wrapped
	}

	return settings, nil
}
