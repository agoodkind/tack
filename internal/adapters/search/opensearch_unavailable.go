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

// `pinnedModelCause` classifies a mismatch only in model state as `ErrEngineUnavailable` because public queries require a `DEPLOYED` model.
func pinnedModelCause(model registeredModel) error {
	mismatches := modelMismatches(model)
	if len(mismatches) == 0 {
		return nil
	}
	joined := errors.Join(mismatches...)
	deployed := model
	deployed.State = deployedModelState
	if len(modelMismatches(deployed)) > 0 {
		return joined
	}
	return unavailableError{err: joined}
}
