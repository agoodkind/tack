package foundationdb

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/clock"
)

// attemptErrorCodeLimit limits the number of attempt error codes that one
// transactionAttempts records.
const attemptErrorCodeLimit = 16

// transactionNotCommitted identifies the FoundationDB error not_committed,
// which an attempt returns when it conflicts with another transaction.
const transactionNotCommitted = 1020

// transactionAttempts counts attempts for one runNodeAttempts transaction.
// It records each attempt's FoundationDB error code that runNodeAttempts
// passes to OnError, up to attemptErrorCodeLimit. It counts later codes and
// records whether any attempt ended with code 1020 (not_committed).
type transactionAttempts struct {
	created    time.Time
	count      int
	codes      []string
	omitted    int
	conflicted bool
}

// newTransactionAttempts starts the record of a transaction created at
// created.
func newTransactionAttempts(created time.Time) *transactionAttempts {
	return &transactionAttempts{created: created, count: 0, codes: nil, omitted: 0, conflicted: false}
}

// start counts one more attempt.
func (attempts *transactionAttempts) start() {
	attempts.count++
}

// recordError keeps code until the list contains attemptErrorCodeLimit codes
// and counts every later code as omitted. It records a conflict when code is
// 1020.
func (attempts *transactionAttempts) recordError(code int) {
	if code == transactionNotCommitted {
		attempts.conflicted = true
	}
	if len(attempts.codes) < attemptErrorCodeLimit {
		attempts.codes = append(attempts.codes, strconv.Itoa(code))
		return
	}
	attempts.omitted++
}

// codeList returns the recorded attempt error codes in attempt order,
// separated by commas.
func (attempts *transactionAttempts) codeList() string {
	return strings.Join(attempts.codes, ",")
}

// logAttributes returns the attempt count, elapsed time since transaction
// creation, recorded attempt error codes in attempt order, and omitted code
// count.
func (attempts *transactionAttempts) logAttributes() []slog.Attr {
	return []slog.Attr{
		slog.Int("attempts", attempts.count),
		slog.Duration("elapsed", clock.Now().Sub(attempts.created)),
		slog.String("attempt_error_codes", attempts.codeList()),
		slog.Int("attempt_error_codes_omitted", attempts.omitted),
	}
}

// retryRefusedError is the error of runNodeAttempts when OnError refuses a
// retry. The caller logs it, usually with retryFailure.
type retryRefusedError struct {
	err      error
	attempts *transactionAttempts
}

func (refused *retryRefusedError) Error() string { return refused.err.Error() }

func (refused *retryRefusedError) Unwrap() error { return refused.err }

// conflictTimedOut reports whether the transaction timed out (1031) after at
// least one attempt conflicted (1020).
func (refused *retryRefusedError) conflictTimedOut() bool {
	var databaseErr fdb.Error
	return refused.attempts.conflicted && errors.As(refused.err, &databaseErr) && databaseErr.Code == transactionTimedOut
}
