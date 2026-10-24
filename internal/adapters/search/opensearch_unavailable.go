package search

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"syscall"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// unavailableStatuses lists statuses that Tack classifies after the OpenSearch client finishes retrying.
// OpenSearch returns HTTP 429 when it rejects a request, including a new point in time at the
// search.max_open_pit_context limit. A proxy returns the other statuses when an OpenSearch member stops.
var unavailableStatuses = []int{
	http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
}

type unavailableError struct{ err error }

func (e unavailableError) Error() string {
	if e.err == nil {
		return "engine unavailable"
	}
	return e.err.Error()
}

func (e unavailableError) Unwrap() []error {
	return []error{searchdomain.ErrEngineUnavailable, e.err}
}

// engineCause wraps err as [searchdomain.ErrEngineUnavailable] when a connection fails with
// connection refused or a response status matches unavailableStatuses.
// It returns err unchanged otherwise. It also logs each HTTP 429 rejection once.
func engineCause(ctx context.Context, response *opensearch.Response, err error) error {
	refused := errors.Is(err, syscall.ECONNREFUSED)
	rejected := response != nil && slices.Contains(unavailableStatuses, response.StatusCode)
	if !refused && !rejected {
		return err
	}
	if response != nil && response.StatusCode == http.StatusTooManyRequests {
		logEngineRejection(ctx, response.StatusCode, err)
	}
	return unavailableError{err: err}
}

// logEngineRejection logs search.engine.rejected at Error level with the error text, HTTP status,
// and OpenSearch error type. The error type is empty unless the response body contains a structured
// OpenSearch error.
func logEngineRejection(ctx context.Context, status int, err error) {
	errorType := ""
	var structured *opensearch.StructError
	if errors.As(err, &structured) {
		errorType = structured.Err.Type
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.engine.rejected", slog.String("err", err.Error()),
		slog.Int("status", status), slog.String("error_type", errorType))
}
