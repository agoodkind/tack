package config

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
	"goodkind.io/tack/internal/telemetry"
)

const (
	searchQueryByteMaximum    = 4096
	searchResponseByteMinimum = 1024
	searchResponseByteMaximum = 1 << 20
	searchTokenByteMaximum    = 256 << 10
	searchBatchMaximum        = 100
	searchBatchCountMaximum   = 4
	searchResultMaximum       = 25
	searchSessionMaximum      = 24 * time.Hour
	searchDeadlineMaximum     = time.Minute
	searchCursorKeyMinimum    = 32
	searchConcurrencyMaximum  = 256
)

// SearchQuerySettings contains the bounded environment-backed settings of
// public ranked search. The cursor key authenticates continuation cursors.
// Concurrency is the number of searches one process runs at once.
// AdmissionWait bounds how long a search waits for one of those slots, and
// it is shorter than RequestDeadline.
//
// IdleTimeout sets the session inactivity deadline and the point-in-time keep-alive.
// Each successful page renews both. OPENSEARCH_SESSION_IDLE_TIMEOUT accepts a Go duration,
// such as 2m or 90s. The Go default is 2 minutes when the variable is empty. Deployments set
// the value through the Configs variable tack_search_session_idle_timeout.
// The value must be positive and must not exceed AbsoluteTimeout.
// A change takes effect after the app process restarts.
type SearchQuerySettings struct {
	MaxQueryBytes    int           `env:"OPENSEARCH_QUERY_MAX_BYTES"          envDefault:"1024"`
	MaxResponseBytes int           `env:"OPENSEARCH_RESPONSE_MAX_BYTES"       envDefault:"16384"`
	MaxTokenBytes    int           `env:"OPENSEARCH_SESSION_TOKEN_MAX_BYTES"  envDefault:"65536"`
	BatchSize        int           `env:"OPENSEARCH_QUERY_BATCH_SIZE"         envDefault:"100"`
	MaxBatches       int           `env:"OPENSEARCH_QUERY_MAX_BATCHES"        envDefault:"4"`
	MaxResults       int           `env:"OPENSEARCH_QUERY_MAX_RESULTS"        envDefault:"25"`
	IdleTimeout      time.Duration `env:"OPENSEARCH_SESSION_IDLE_TIMEOUT"     envDefault:"2m"`
	AbsoluteTimeout  time.Duration `env:"OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT" envDefault:"2h"`
	RequestDeadline  time.Duration `env:"OPENSEARCH_QUERY_DEADLINE"           envDefault:"20s"`
	Concurrency      int           `env:"OPENSEARCH_QUERY_CONCURRENCY"        envDefault:"4"`
	AdmissionWait    time.Duration `env:"OPENSEARCH_QUERY_ADMISSION_WAIT"     envDefault:"5s"`
	CursorKey        string        `env:"OPENSEARCH_CURSOR_KEY,required"`
}

// LoadSearchQuerySettings parses and validates the public search environment.
func LoadSearchQuerySettings(ctx context.Context) (SearchQuerySettings, error) {
	environment, err := secretEnvironment([]string{"OPENSEARCH_CURSOR_KEY"})
	if err != nil {
		return SearchQuerySettings{}, err
	}
	var settings SearchQuerySettings
	if err := env.ParseWithOptions(&settings, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("parse search query settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.query.config_failed", slog.String("err", wrapped.Error()))
		return SearchQuerySettings{}, wrapped
	}
	if err := settings.Validate(); err != nil {
		wrapped := fmt.Errorf("validate search query settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.query.config_failed", slog.String("err", wrapped.Error()))
		return SearchQuerySettings{}, wrapped
	}
	return settings, nil
}

// CursorKeyBytes decodes the standard base64 cursor key.
func (s SearchQuerySettings) CursorKeyBytes() ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(s.CursorKey)
	if err != nil {
		return nil, errors.New("decode search cursor key: " + err.Error())
	}
	if len(decoded) < searchCursorKeyMinimum {
		return nil, fmt.Errorf("search cursor key must decode to at least %d bytes", searchCursorKeyMinimum)
	}
	return decoded, nil
}

// Validate enforces the public request, response, session, batch, result,
// deadline, and admission limits.
func (s SearchQuerySettings) Validate() error {
	if s.MaxQueryBytes < 1 || s.MaxQueryBytes > searchQueryByteMaximum {
		return fmt.Errorf("search query bytes must be between 1 and %d", searchQueryByteMaximum)
	}
	if s.MaxResponseBytes < searchResponseByteMinimum || s.MaxResponseBytes > searchResponseByteMaximum {
		return fmt.Errorf("search response bytes must be between %d and %d", searchResponseByteMinimum, searchResponseByteMaximum)
	}
	if s.MaxTokenBytes < 1 || s.MaxTokenBytes > searchTokenByteMaximum {
		return fmt.Errorf("search session token bytes must be between 1 and %d", searchTokenByteMaximum)
	}
	if s.BatchSize < 1 || s.BatchSize > searchBatchMaximum || s.MaxBatches < 1 || s.MaxBatches > searchBatchCountMaximum {
		return fmt.Errorf("search batches must contain 1 to %d matches, with 1 to %d batches per response", searchBatchMaximum, searchBatchCountMaximum)
	}
	if s.MaxResults < 1 || s.MaxResults > searchResultMaximum {
		return fmt.Errorf("search results must be between 1 and %d", searchResultMaximum)
	}
	if s.IdleTimeout <= 0 || s.AbsoluteTimeout < s.IdleTimeout || s.AbsoluteTimeout > searchSessionMaximum {
		return errors.New("search session idle timeout must be positive and must not exceed the absolute timeout of at most one day")
	}
	if s.RequestDeadline <= 0 || s.RequestDeadline > searchDeadlineMaximum {
		return errors.New("search query deadline must be positive and must not exceed one minute")
	}
	if err := s.validateAdmission(); err != nil {
		return err
	}
	if _, err := s.CursorKeyBytes(); err != nil {
		return err
	}
	return nil
}

// validateAdmission enforces the process search slots and requires the
// admission wait to end before the search query deadline.
func (s SearchQuerySettings) validateAdmission() error {
	if s.Concurrency < 1 || s.Concurrency > searchConcurrencyMaximum {
		return fmt.Errorf("search query concurrency must be between 1 and %d", searchConcurrencyMaximum)
	}
	if s.AdmissionWait <= 0 || s.AdmissionWait >= s.RequestDeadline {
		return errors.New("search admission wait must be positive and shorter than the search query deadline")
	}
	return nil
}
