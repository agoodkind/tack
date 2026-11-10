package testenv

import (
	"errors"
	"strconv"
)

var (
	errFixtureCreateEvidenceLimit   = errors.New("fixture creation evidence exceeds 16 MiB")
	errDiskReadinessResponseMissing = errors.New(
		"the create-index readiness settings request did not return a response",
	)
	errDiskReadinessBlockMissing = errors.New(
		"the readiness response omitted cluster.blocks.create_index",
	)
)

type diskReadinessStatusError struct{ statusCode int }

func (err *diskReadinessStatusError) Error() string {
	return "read the create-index readiness settings: HTTP " + strconv.Itoa(err.statusCode)
}
