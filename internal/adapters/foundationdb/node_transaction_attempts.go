package foundationdb

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"goodkind.io/tack/internal/clock"
)

// attemptErrorCodeLimit is the largest number of attempt error codes that
// one transactionAttempts keeps.
const attemptErrorCodeLimit = 16

// transactionAttempts counts the attempts of one runNodeMutation transaction
// and keeps the FoundationDB error code of each attempt that the function
// passed to OnError, up to attemptErrorCodeLimit codes. Code 1020
// (not_committed) in the list means an attempt conflicted with another
// transaction.
type transactionAttempts struct {
	created time.Time
	count   int
	codes   []string
}

// newTransactionAttempts starts the record of a transaction created now.
func newTransactionAttempts() *transactionAttempts {
	return &transactionAttempts{created: clock.Now(), count: 0, codes: nil}
}

// start counts one more attempt.
func (attempts *transactionAttempts) start() {
	attempts.count++
}

// recordError keeps code until the list contains attemptErrorCodeLimit
// codes.
func (attempts *transactionAttempts) recordError(code int) {
	if len(attempts.codes) < attemptErrorCodeLimit {
		attempts.codes = append(attempts.codes, strconv.Itoa(code))
	}
}

// logAttributes returns the attempt count, the time since the transaction
// was created, and the kept attempt error codes in attempt order.
func (attempts *transactionAttempts) logAttributes() []slog.Attr {
	return []slog.Attr{
		slog.Int("attempts", attempts.count),
		slog.Duration("elapsed", clock.Now().Sub(attempts.created)),
		slog.String("attempt_error_codes", strings.Join(attempts.codes, ",")),
	}
}
