package search

import (
	"errors"
	"net/http"
	"slices"
	"syscall"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// A proxy returns these statuses when an OpenSearch member is stopped.
var unavailableStatuses = []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}

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

// tack_search returns "Search is temporarily unavailable." for searchdomain.ErrEngineUnavailable.
func engineCause(response *opensearch.Response, err error) error {
	refused := errors.Is(err, syscall.ECONNREFUSED)
	rejected := response != nil && slices.Contains(unavailableStatuses, response.StatusCode)
	if refused || rejected {
		return unavailableError{err: err}
	}
	return err
}
