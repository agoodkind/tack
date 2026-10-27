package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// maxRecordedRetryCodes bounds the retried FoundationDB error codes that
// one session transaction failure log lists.
const maxRecordedRetryCodes = 8

// sessionAttempts records the attempts of one session transaction for its
// failure log.
type sessionAttempts struct {
	source       clock.Clock
	created      time.Time
	attemptStart time.Time
	count        int
	retriedCodes []int
	omittedCodes int
}

func newSessionAttempts(source clock.Clock) *sessionAttempts {
	now := source.Now()
	return &sessionAttempts{source: source, created: now, attemptStart: now, count: 0, retriedCodes: nil, omittedCodes: 0}
}

// begin starts one attempt.
func (a *sessionAttempts) begin() {
	a.attemptStart = a.source.Now()
	a.count++
}

// retried records the FoundationDB error code of an attempt that the retry
// loop retries. It keeps the first maxRecordedRetryCodes codes and counts
// the rest.
func (a *sessionAttempts) retried(code int) {
	if len(a.retriedCodes) >= maxRecordedRetryCodes {
		a.omittedCodes++
		return
	}
	a.retriedCodes = append(a.retriedCodes, code)
}

// attributes returns the failure log attributes of the attempts.
func (a *sessionAttempts) attributes() []slog.Attr {
	now := a.source.Now()
	codes := make([]string, 0, len(a.retriedCodes))
	for _, code := range a.retriedCodes {
		codes = append(codes, strconv.Itoa(code))
	}
	return []slog.Attr{
		slog.Int("attempts", a.count),
		slog.Duration("transaction_elapsed", now.Sub(a.created)),
		slog.Duration("last_attempt_elapsed", now.Sub(a.attemptStart)),
		slog.String("retried_error_codes", strings.Join(codes, ",")),
		slog.Int("retried_error_codes_omitted", a.omittedCodes),
	}
}

// transactSession runs apply and commits it, retrying retryable
// FoundationDB errors. Each attempt bounds its timeout by the context
// deadline, measured with source. Expected session outcomes return without
// an error log. A failure log reports the attempts, the elapsed times, and
// the retried error codes.
func transactSession(ctx context.Context, db fdb.Database, source clock.Clock, operation string, sessionID uuid.UUID, apply func(fdb.Transaction) error) error {
	transaction, err := db.CreateTransaction()
	if err != nil {
		return sessionFailure(ctx, operation, sessionID, err)
	}
	defer transaction.Cancel()
	attempts := newSessionAttempts(source)
	for {
		if err := ctx.Err(); err != nil {
			return sessionFailure(ctx, operation, sessionID, err, attempts.attributes()...)
		}
		attempts.begin()
		timeout := searchTransactionTimeout
		if deadline, exists := ctx.Deadline(); exists {
			timeout = max(time.Millisecond, min(timeout, deadline.Sub(source.Now())))
		}
		if err := transaction.Options().SetTimeout(max(int64(timeout/time.Millisecond), 1)); err != nil {
			return sessionFailure(ctx, operation, sessionID, err, attempts.attributes()...)
		}
		err := apply(transaction)
		if err == nil {
			err = transaction.Commit().Get()
		}
		if err == nil {
			return nil
		}
		if isSessionOutcome(err) {
			return sessionStepError{operation: operation + " for session " + sessionID.String(), err: err}
		}
		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return sessionFailure(ctx, operation, sessionID, err, attempts.attributes()...)
		}
		if retryError := transaction.OnError(databaseError).Get(); retryError != nil {
			return sessionFailure(ctx, operation, sessionID, retryError, attempts.attributes()...)
		}
		attempts.retried(databaseError.Code)
	}
}

// searchTimeoutError is a public search read that ended with FoundationDB
// error 1031 (transaction_timed_out). It keeps the text of err and also wraps
// [searchdomain.ErrEngineUnavailable].
type searchTimeoutError struct{ err error }

func (e searchTimeoutError) Error() string { return e.err.Error() }

func (e searchTimeoutError) Unwrap() []error {
	return []error{e.err, searchdomain.ErrEngineUnavailable}
}

// classifySearchTimeout returns err as a searchTimeoutError when err wraps
// FoundationDB error 1031 and returns every other err unchanged.
func classifySearchTimeout(err error) error {
	var databaseError fdb.Error
	if errors.As(err, &databaseError) && databaseError.Code == transactionTimedOut {
		return searchTimeoutError{err: err}
	}
	return err
}

// sessionFailure logs and wraps one session storage failure with the
// operation name and the session ID. It logs details in the transaction
// group, which the log omits when details is empty. A failure with
// FoundationDB error 1031 also wraps [searchdomain.ErrEngineUnavailable].
func sessionFailure(ctx context.Context, operation string, sessionID uuid.UUID, err error, details ...slog.Attr) error {
	wrapped := fmt.Errorf("%s for session %s: %w", operation, sessionID, classifySearchTimeout(err))
	slog.ErrorContext(ctx, "search.session.storage_failed", slog.String("err", wrapped.Error()),
		slog.String("operation", operation), slog.String("session_id", sessionID.String()),
		slog.Any("transaction", slog.GroupValue(details...)))
	return wrapped
}
