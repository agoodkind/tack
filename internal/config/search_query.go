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

// SearchQuerySettings bounds public ranked search through environment settings.
// The cursor key authenticates continuation cursors. Concurrency limits
// simultaneous searches in one process. AdmissionWait bounds the wait for a
// slot and must remain shorter than RequestDeadline.
//
// IdleTimeout sets the session inactivity deadline and point-in-time
// keep-alive. Each successful page renews both. OPENSEARCH_SESSION_IDLE_TIMEOUT
// accepts Go durations such as 2m or 90s. An empty variable uses the two-minute
// default. The original Configs integration used
// tack_search_session_idle_timeout. The value must be positive and must not
// exceed AbsoluteTimeout. Changes take effect after the app restarts.
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

// LoadSearchQuerySettings reads the public ranked search settings.
//
// LoadSearchQuerySettings resolves the cursor key's secret reference, parses
// the environment, and calls [SearchQuerySettings.Validate]. Failures return an
// empty settings value and an error.
//
// The cursor key accepts these sources:
//
//   - OPENSEARCH_CURSOR_KEY supplies the key directly.
//   - OPENSEARCH_CURSOR_KEY_FILE supplies the path to the key.
func LoadSearchQuerySettings(ctx context.Context) (SearchQuerySettings, error) {
	environment, err := secretEnvironment([]string{"OPENSEARCH_CURSOR_KEY"})
	if err != nil {
		return SearchQuerySettings{}, err
	}

	var settings SearchQuerySettings
	if err := env.ParseWithOptions(&settings, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("parse search query settings: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.query.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchQuerySettings{}, wrapped
	}

	if err := settings.Validate(); err != nil {
		wrapped := fmt.Errorf("validate search query settings: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.query.config_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchQuerySettings{}, wrapped
	}

	return settings, nil
}

// CursorKeyBytes decodes the cursor key in [SearchQuerySettings.CursorKey] from
// standard base64.
//
// SearchQuerySettings.CursorKeyBytes rejects invalid base64 and keys that
// decode to fewer than 32 bytes.
//
// The return values distinguish these outcomes:
//
//   - A valid key returns the decoded bytes.
//   - An invalid or short key returns nil and an error.
func (s SearchQuerySettings) CursorKeyBytes() ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(s.CursorKey)
	if err != nil {
		return nil, errors.New("decode search cursor key: " + err.Error())
	}

	if len(decoded) < searchCursorKeyMinimum {
		return nil, fmt.Errorf(
			"search cursor key must decode to at least %d bytes",
			searchCursorKeyMinimum,
		)
	}

	return decoded, nil
}

// Validate checks the public search limits in [SearchQuerySettings].
//
// SearchQuerySettings.Validate checks request, response, session, batch,
// result, deadline, and admission bounds and validates the cursor key. It
// returns the first error it finds.
//
// Validation requires these relationships:
//
//   - IdleTimeout must be positive and must not exceed AbsoluteTimeout.
//   - AdmissionWait must be positive and shorter than RequestDeadline.
func (s SearchQuerySettings) Validate() error {
	if s.MaxQueryBytes < 1 || s.MaxQueryBytes > searchQueryByteMaximum {
		return fmt.Errorf("search query bytes must be between 1 and %d", searchQueryByteMaximum)
	}

	if s.MaxResponseBytes < searchResponseByteMinimum ||
		s.MaxResponseBytes > searchResponseByteMaximum {
		return fmt.Errorf(
			"search response bytes must be between %d and %d",
			searchResponseByteMinimum,
			searchResponseByteMaximum,
		)
	}

	if s.MaxTokenBytes < 1 || s.MaxTokenBytes > searchTokenByteMaximum {
		return fmt.Errorf(
			"search session token bytes must be between 1 and %d",
			searchTokenByteMaximum,
		)
	}

	if s.BatchSize < 1 || s.BatchSize > searchBatchMaximum || s.MaxBatches < 1 ||
		s.MaxBatches > searchBatchCountMaximum {
		return fmt.Errorf(
			"search batches must contain 1 to %d matches, with 1 to %d batches per response",
			searchBatchMaximum,
			searchBatchCountMaximum,
		)
	}

	if s.MaxResults < 1 || s.MaxResults > searchResultMaximum {
		return fmt.Errorf("search results must be between 1 and %d", searchResultMaximum)
	}

	if s.IdleTimeout <= 0 || s.AbsoluteTimeout < s.IdleTimeout ||
		s.AbsoluteTimeout > searchSessionMaximum {
		return errors.New(
			"search session idle timeout must be positive and must not exceed the absolute timeout of at most one day",
		)
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

// validateAdmission checks the process search-slot bounds and requires a
// positive admission wait shorter than the query deadline.
func (s SearchQuerySettings) validateAdmission() error {
	if s.Concurrency < 1 || s.Concurrency > searchConcurrencyMaximum {
		return fmt.Errorf(
			"search query concurrency must be between 1 and %d",
			searchConcurrencyMaximum,
		)
	}

	if s.AdmissionWait <= 0 || s.AdmissionWait >= s.RequestDeadline {
		return errors.New(
			"search admission wait must be positive and shorter than the search query deadline",
		)
	}

	return nil
}
