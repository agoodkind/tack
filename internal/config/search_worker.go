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

// SearchReplacementPauseLimit bounds the wait for a green target, a target with
// healthy primary and replica shards. The wait starts when replacement pauses
// claims, or when a full replacement begins, including the wait for its first
// claim. A create, split, or switch step fails the replacement if the target
// remains unhealthy past this limit. The next claim restores source writes,
// deletes the target, and resumes claims.
const SearchReplacementPauseLimit = 10 * time.Minute

// SearchWriteCompletionHorizon sets the wait after abandonment before a claim
// may resend a page that remains absent from its recorded index.
const SearchWriteCompletionHorizon = 90 * time.Second

// SearchWorkerSettings bounds indexing work through environment settings.
type SearchWorkerSettings struct {
	PageBytes        int           `env:"OPENSEARCH_PAGE_BYTES"               envDefault:"4096"`
	MaxPages         int           `env:"OPENSEARCH_WORKER_MAX_PAGES"         envDefault:"32"`
	MaxBytes         int           `env:"OPENSEARCH_WORKER_MAX_BYTES"         envDefault:"5242880"`
	SliceBudget      time.Duration `env:"OPENSEARCH_WORKER_SLICE_BUDGET"      envDefault:"2s"`
	OperationTimeout time.Duration `env:"OPENSEARCH_WORKER_OPERATION_TIMEOUT" envDefault:"10s"`
	Lease            time.Duration `env:"OPENSEARCH_WORKER_LEASE"             envDefault:"30s"`
	IdleInterval     time.Duration `env:"OPENSEARCH_WORKER_IDLE_INTERVAL"     envDefault:"250ms"`
	Concurrency      int           `env:"OPENSEARCH_WORKER_CONCURRENCY"       envDefault:"1"`
	// ClassWeights sets each class's share of worker time when several classes have
	// pending work. Every scheduled class requires a weight of at least one; the
	// map must not include other classes.
	ClassWeights map[string]int `env:"OPENSEARCH_WORK_CLASS_WEIGHTS" envDefault:"live:4,access:2,cleanup:2,rescan:1,rollout:1,rebuild:1,copy:2" envKeyValSeparator:":"`
	// ReplacementPauseLimit bounds the wait for a green replacement target. It does
	// not have an environment variable. [LoadSearchWorkerSettings] sets it to
	// [SearchReplacementPauseLimit], and tests use shorter limits.
	ReplacementPauseLimit time.Duration
	// WriteCompletionHorizon bounds the wait for an abandoned write when its page
	// remains absent. It does not have an environment variable.
	// [LoadSearchWorkerSettings] sets it to [SearchWriteCompletionHorizon], and
	// tests use other horizons.
	WriteCompletionHorizon time.Duration
}

// LoadSearchWorkerSettings reads the indexing worker environment settings.
//
// LoadSearchWorkerSettings assigns the fixed replacement and write-completion
// limits, then calls [SearchWorkerSettings.Validate]. Parsing or validation
// failures return an empty settings value and an error.
//
// The fixed settings use these constants:
//
//   - ReplacementPauseLimit uses [SearchReplacementPauseLimit].
//   - WriteCompletionHorizon uses [SearchWriteCompletionHorizon].
func LoadSearchWorkerSettings(ctx context.Context) (SearchWorkerSettings, error) {
	var settings SearchWorkerSettings
	if err := env.Parse(&settings); err != nil {
		wrapped := fmt.Errorf("parse search worker settings: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.worker.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchWorkerSettings{}, wrapped
	}

	settings.ReplacementPauseLimit = SearchReplacementPauseLimit
	settings.WriteCompletionHorizon = SearchWriteCompletionHorizon
	if err := settings.Validate(); err != nil {
		wrapped := fmt.Errorf("validate search worker settings: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.worker.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchWorkerSettings{}, wrapped
	}

	return settings, nil
}

// Validate checks the indexing limits in [SearchWorkerSettings].
//
// SearchWorkerSettings.Validate checks page and byte bounds, timing,
// concurrency, and scheduled class weights. It returns the first error it
// finds.
//
// Validation requires these timing relationships:
//
//   - Lease must exceed SliceBudget plus OperationTimeout.
//   - ReplacementPauseLimit must exceed Lease.
//   - WriteCompletionHorizon must exceed OperationTimeout and remain shorter
//     than ReplacementPauseLimit.
func (s SearchWorkerSettings) Validate() error {
	if s.PageBytes < searchPageByteMinimum || s.PageBytes > searchPageByteMaximum {
		return fmt.Errorf(
			"search page bytes must be between %d and %d",
			searchPageByteMinimum,
			searchPageByteMaximum,
		)
	}

	if s.MaxPages <= 0 || s.MaxPages > searchWorkerPageMaximum {
		return fmt.Errorf("search worker pages must be between 1 and %d", searchWorkerPageMaximum)
	}

	if s.MaxBytes <= 0 || s.MaxBytes > searchWorkerByteMaximum {
		return fmt.Errorf("search worker bytes must be between 1 and %d", searchWorkerByteMaximum)
	}

	if s.SliceBudget <= 0 || s.SliceBudget > 2*time.Second {
		return errors.New(
			"search worker slice budget must be positive and must not exceed two seconds",
		)
	}

	if s.OperationTimeout <= 0 || s.OperationTimeout > 10*time.Second {
		return errors.New(
			"search worker operation timeout must be positive and must not exceed ten seconds",
		)
	}

	if s.Lease <= s.SliceBudget+s.OperationTimeout {
		return errors.New(
			"search worker lease must exceed the slice budget plus the operation timeout",
		)
	}

	if s.ReplacementPauseLimit <= s.Lease {
		return errors.New("search replacement pause limit must exceed the worker lease")
	}

	if s.WriteCompletionHorizon <= s.OperationTimeout ||
		s.WriteCompletionHorizon >= s.ReplacementPauseLimit {
		return errors.New(
			"search write completion horizon must exceed the operation " +
				"timeout and must be shorter than the replacement pause limit",
		)
	}

	if s.IdleInterval <= 0 || s.Concurrency <= 0 || s.Concurrency > searchWorkerMaximum {
		return fmt.Errorf(
			"search worker idle interval must be positive, and concurrency must be between 1 and %d",
			searchWorkerMaximum,
		)
	}

	return validateClassWeights(s.ClassWeights)
}

// validateClassWeights requires a weight for every scheduled search work class
// and rejects other class names.
func validateClassWeights(weights map[string]int) error {
	classes := search.ScheduledWorkClasses()
	for _, class := range classes {
		weight, exists := weights[string(class)]
		if !exists {
			return fmt.Errorf("search work class %q requires a weight", class)
		}

		if weight < 1 || weight > searchClassWeightMax {
			return fmt.Errorf(
				"search work class %q weight must be between 1 and %d",
				class,
				searchClassWeightMax,
			)
		}
	}

	for class := range weights {
		if !slices.Contains(classes, search.WorkClass(class)) {
			return fmt.Errorf("search work class %q is not a scheduled class", class)
		}
	}

	return nil
}
