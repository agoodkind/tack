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

// attemptErrorCodeLimit is the largest number of attempt error codes that
// one transactionAttempts keeps.
const attemptErrorCodeLimit = 16

// transactionNotCommitted identifies the FoundationDB error not_committed,
// which an attempt returns when it conflicts with another transaction.
const transactionNotCommitted = 1020

// transactionAttempts counts the attempts of one runNodeAttempts transaction
// and keeps the FoundationDB error code of each attempt that the function
// passed to OnError, up to attemptErrorCodeLimit codes. It also records
// whether any attempt ended with code 1020 (not_committed).
type transactionAttempts struct {
	created    time.Time
	count      int
	codes      []string
	conflicted bool
}

// newTransactionAttempts starts the record of a transaction created now.
func newTransactionAttempts() *transactionAttempts {
	return &transactionAttempts{created: clock.Now(), count: 0, codes: nil, conflicted: false}
}

// start counts one more attempt.
func (attempts *transactionAttempts) start() {
	attempts.count++
}

// recordError keeps code until the list contains attemptErrorCodeLimit
// codes, and records a conflict when code is 1020.
func (attempts *transactionAttempts) recordError(code int) {
	if code == transactionNotCommitted {
		attempts.conflicted = true
	}
	if len(attempts.codes) < attemptErrorCodeLimit {
		attempts.codes = append(attempts.codes, strconv.Itoa(code))
	}
}

// codeList returns the kept attempt error codes in attempt order, separated
// by commas.
func (attempts *transactionAttempts) codeList() string {
	return strings.Join(attempts.codes, ",")
}

// logAttributes returns the attempt count, the time since the transaction
// was created, and the kept attempt error codes in attempt order.
func (attempts *transactionAttempts) logAttributes() []slog.Attr {
	return []slog.Attr{
		slog.Int("attempts", attempts.count),
		slog.Duration("elapsed", clock.Now().Sub(attempts.created)),
		slog.String("attempt_error_codes", attempts.codeList()),
	}
}

// retryRefusedError is the error of runNodeAttempts when OnError refuses a retry.
// The caller logs it, usually with retryFailure.
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
