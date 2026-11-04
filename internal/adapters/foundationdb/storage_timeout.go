package foundationdb

import (
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
)

// storageTimeoutError represents a read that ended with FoundationDB error
// 1031, the transaction timeout. It preserves the text of err and wraps
// domain.ErrStorageTimeout.
type storageTimeoutError struct{ err error }

func (e storageTimeoutError) Error() string { return e.err.Error() }

func (e storageTimeoutError) Unwrap() []error {
	return []error{e.err, domain.ErrStorageTimeout}
}

// classifyStorageTimeout returns a storageTimeoutError when err wraps
// FoundationDB error 1031, the transaction timeout. It returns every other
// err unchanged.
func classifyStorageTimeout(err error) error {
	var databaseError fdb.Error
	if errors.As(err, &databaseError) && databaseError.Code == transactionTimedOut {
		return storageTimeoutError{err: err}
	}
	return err
}
