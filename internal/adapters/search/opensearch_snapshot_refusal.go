package search

import (
	"errors"
	"net/http"
	"strings"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
)

const (
	snapshotCreateAction = "indices:data/read/point_in_time/create"
	snapshotDeleteAction = "indices:data/read/point_in_time/delete"
)

type snapshotRefusalType string

const (
	snapshotRequestValidationRefusal snapshotRefusalType = "action_request_validation_exception"
	snapshotMissingIndexRefusal      snapshotRefusalType = "index_not_found_exception"
	snapshotSecurityRefusal          snapshotRefusalType = "security_exception"
)

// Public request validation, index resolution, and action filters run
// before the point-in-time action. An error from an internal point-in-time
// update does not prove refusal.
func snapshotEffectRefused(response *opensearch.Response, err error) bool {
	if response == nil || !response.IsError() {
		return false
	}

	var refusal *opensearch.StructError
	if !errors.As(err, &refusal) || refusal.Status != response.StatusCode {
		return false
	}

	switch snapshotRefusalType(refusal.Err.Type) {
	case snapshotRequestValidationRefusal:
		return response.StatusCode == http.StatusBadRequest
	case snapshotMissingIndexRefusal:
		return response.StatusCode == http.StatusNotFound
	case snapshotSecurityRefusal:
		for _, action := range []string{snapshotCreateAction, snapshotDeleteAction} {
			if response.StatusCode == http.StatusForbidden &&
				strings.HasPrefix(refusal.Err.Reason, "no permissions for ["+action+"]") {
				return true
			}
		}
	}

	return false
}
