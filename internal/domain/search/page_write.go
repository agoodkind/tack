package search

import (
	"errors"
	"time"

	"goodkind.io/tack/internal/domain/node"
)

// ErrPageWritePending means a pending page remains absent from its recorded
// index while its deadline has not passed. The adapter does not send another
// write for that page in that index.
var ErrPageWritePending = errors.New("an abandoned write of the search page is still pending")

// Failure is one failed slice of a claimed work item. Counted is false for
// a failed OpenSearch operation, and such a failure never counts toward the
// attempt limit of the work.
type Failure struct {
	Message string
	Counted bool
	// RetryAfter is the minimum wait before the next claim of the work.
	RetryAfter time.Duration
	// Pending records the page write the worker stopped waiting for at
	// OperationTimeout. Release stores it in the release transaction. Other
	// failures leave it nil.
	Pending *PendingWrite `exhaustruct:"optional"`
}

// WriteIntent binds one page write to its claimed generation and target.
type WriteIntent struct {
	Work       Work
	Page       node.ContentPage
	DocumentID string
	// Pending is an unexpired pending write for DocumentID. Put does not create
	// an absent page in Pending.Index while Pending is non-nil.
	Pending *PendingWrite `exhaustruct:"optional"`
	// MirrorMissed identifies an owed replacement write of DocumentID and its deadline.
	// The worker suppresses the mirror request while this is non-nil.
	// CompletePage commits the owed copy with the serving cursor.
	MirrorMissed *PendingWrite `exhaustruct:"optional"`
}

// PendingWrite records a page write the worker stopped waiting for at
// OperationTimeout. Its deadline limits how long claims wait for an absent
// page. OpenSearch may complete the request after the worker stops waiting.
type PendingWrite struct {
	DocumentID string
	// Index is the physical index of the abandoned request.
	Index   string
	Ordinal uint64
	// Generation is the claimed generation of the abandoned write.
	Generation  int64
	AbandonedAt time.Time
	// Deadline is AbandonedAt plus WriteCompletionHorizon.
	Deadline time.Time
}

// PageWriteError wraps a page write error with the physical index of the
// failed request.
type PageWriteError struct {
	Index string
	Err   error
}

// Error returns the message of the write error.
func (e *PageWriteError) Error() string { return e.Err.Error() }

// Unwrap returns the write error.
func (e *PageWriteError) Unwrap() error { return e.Err }
