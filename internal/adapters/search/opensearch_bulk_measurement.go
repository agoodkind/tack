package search

import (
	"errors"
	"log/slog"
	"time"
)

// bulkMeasurement stores the round-trip duration, page count, and request
// size in bytes for one bulk request.
type bulkMeasurement struct {
	duration time.Duration
	pages    int
	bytes    int
}

// attr returns the measurement as the bulk log attribute group with
// duration_ms, pages, and bytes as keys.
func (m bulkMeasurement) attr() slog.Attr {
	return slog.Group("bulk", slog.Int64("duration_ms", m.duration.Milliseconds()),
		slog.Int("pages", m.pages), slog.Int("bytes", m.bytes))
}

// bulkRequestError wraps err with the measurement of the failed bulk
// request. Its Error method returns err's message, and its Unwrap method
// returns err.
type bulkRequestError struct {
	err      error
	measured bulkMeasurement
}

func (e bulkRequestError) Error() string { return e.err.Error() }

func (e bulkRequestError) Unwrap() error { return e.err }

// bulkAttr returns the bulk group from the bulkRequestError in err.
// If err does not contain a bulkRequestError, bulkAttr returns the zero
// [slog.Attr], which slog handlers ignore.
func bulkAttr(err error) slog.Attr {
	var failed bulkRequestError
	if !errors.As(err, &failed) {
		return slog.Attr{}
	}
	return failed.measured.attr()
}
