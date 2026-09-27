package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"
	"goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	searchPageByteMinimum   = 16
	searchPageByteMaximum   = 4096
	searchWorkerPageMaximum = 32
	searchWorkerByteMaximum = 5 * 1024 * 1024
	searchWorkerMaximum     = 32
	searchClassWeightMax    = 16
)

// SearchWorkerSettings contains bounded environment-backed indexing settings.
type SearchWorkerSettings struct {
	PageBytes        int           `env:"OPENSEARCH_PAGE_BYTES"               envDefault:"4096"`
	MaxPages         int           `env:"OPENSEARCH_WORKER_MAX_PAGES"         envDefault:"32"`
	MaxBytes         int           `env:"OPENSEARCH_WORKER_MAX_BYTES"         envDefault:"5242880"`
	SliceBudget      time.Duration `env:"OPENSEARCH_WORKER_SLICE_BUDGET"      envDefault:"2s"`
	OperationTimeout time.Duration `env:"OPENSEARCH_WORKER_OPERATION_TIMEOUT" envDefault:"10s"`
	Lease            time.Duration `env:"OPENSEARCH_WORKER_LEASE"             envDefault:"30s"`
	IdleInterval     time.Duration `env:"OPENSEARCH_WORKER_IDLE_INTERVAL"     envDefault:"250ms"`
	Concurrency      int           `env:"OPENSEARCH_WORKER_CONCURRENCY"       envDefault:"1"`
	// ClassWeights sets how many turns each work class receives in one
	// scheduling rotation. It requires a weight of at least one for every
	// scheduled class and no other class.
	ClassWeights map[string]int `env:"OPENSEARCH_WORK_CLASS_WEIGHTS" envDefault:"live:4,access:2,cleanup:2,rescan:1" envKeyValSeparator:":"`
}

// LoadSearchWorkerSettings parses and validates the indexing worker environment.
func LoadSearchWorkerSettings(ctx context.Context) (SearchWorkerSettings, error) {
	var settings SearchWorkerSettings
	if err := env.Parse(&settings); err != nil {
		wrapped := fmt.Errorf("parse search worker settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.worker.config_failed", slog.String("err", wrapped.Error()))
		return SearchWorkerSettings{}, wrapped
	}
	if err := settings.Validate(); err != nil {
		wrapped := fmt.Errorf("validate search worker settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.worker.config_failed", slog.String("err", wrapped.Error()))
		return SearchWorkerSettings{}, wrapped
	}
	return settings, nil
}

// Validate enforces the production page, slice, lease, and class limits.
func (s SearchWorkerSettings) Validate() error {
	if s.PageBytes < searchPageByteMinimum || s.PageBytes > searchPageByteMaximum {
		return fmt.Errorf("search page bytes must be between %d and %d", searchPageByteMinimum, searchPageByteMaximum)
	}
	if s.MaxPages <= 0 || s.MaxPages > searchWorkerPageMaximum {
		return fmt.Errorf("search worker pages must be between 1 and %d", searchWorkerPageMaximum)
	}
	if s.MaxBytes <= 0 || s.MaxBytes > searchWorkerByteMaximum {
		return fmt.Errorf("search worker bytes must be between 1 and %d", searchWorkerByteMaximum)
	}
	if s.SliceBudget <= 0 || s.SliceBudget > 2*time.Second {
		return errors.New("search worker slice budget must be positive and must not exceed two seconds")
	}
	if s.OperationTimeout <= 0 || s.OperationTimeout > 10*time.Second {
		return errors.New("search worker operation timeout must be positive and must not exceed ten seconds")
	}
	if s.Lease <= s.SliceBudget+s.OperationTimeout {
		return errors.New("search worker lease must exceed the slice budget plus the operation timeout")
	}
	if s.IdleInterval <= 0 || s.Concurrency <= 0 || s.Concurrency > searchWorkerMaximum {
		return fmt.Errorf("search worker idle interval must be positive and concurrency between 1 and %d", searchWorkerMaximum)
	}
	return validateClassWeights(s.ClassWeights)
}

// validateClassWeights requires one weight for each class a search worker
// schedules and rejects every other class name.
func validateClassWeights(weights map[string]int) error {
	classes := search.ScheduledWorkClasses()
	for _, class := range classes {
		weight, exists := weights[string(class)]
		if !exists {
			return fmt.Errorf("search work class %q requires a weight", class)
		}
		if weight < 1 || weight > searchClassWeightMax {
			return fmt.Errorf("search work class %q weight must be between 1 and %d", class, searchClassWeightMax)
		}
	}
	for class := range weights {
		if !slices.Contains(classes, search.WorkClass(class)) {
			return fmt.Errorf("search work class %q is not a scheduled class", class)
		}
	}
	return nil
}
