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

const transactionTooOld = 1007

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
	return &sessionAttempts{
		source:       source,
		created:      now,
		attemptStart: now,
		count:        0,
		retriedCodes: nil,
		omittedCodes: 0,
	}
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

// transactSession runs apply and commits its changes, retrying retryable
// FoundationDB errors. Each attempt uses source to measure the time
// remaining until the context deadline and bounds its timeout by that time.
// Expected session outcomes return without an error log. The function logs
// at Info when recovery resolved a retirement attempt or a replacement
// ended. A failure log reports the attempts, elapsed times, and retried
// error codes.
func transactSession(
	ctx context.Context,
	db fdb.Database,
	source clock.Clock,
	operation string,
	sessionID uuid.UUID,
	apply func(fdb.Transaction) error,
) error {
	transaction, err := db.CreateTransaction()
	if err != nil {
		return sessionFailure(ctx, operation, sessionID, err)
	}
	defer transaction.Cancel()
	defer forgetSession(transaction)
	attempts := newSessionAttempts(source)
	uncertain := false

	for {
		if err := ctx.Err(); err != nil {
			if attempts.count == 0 {
				err = sessionNotStartedError{err: err}
			}

			return sessionFailure(
				ctx,
				operation,
				sessionID,
				markCommitUncertain(err, uncertain),
				attempts.attributes()...,
			)
		}

		attempts.begin()
		timeout := searchTransactionTimeout
		if deadline, exists := ctx.Deadline(); exists {
			timeout = max(time.Millisecond, min(timeout, deadline.Sub(source.Now())))
		}

		if err := transaction.Options().SetTimeout(
			max(int64(timeout/time.Millisecond), 1),
		); err != nil {
			return sessionFailure(
				ctx,
				operation,
				sessionID,
				markCommitUncertain(err, uncertain),
				attempts.attributes()...,
			)
		}

		err := applySession(transaction, apply)
		if err == nil {
			err = transaction.Commit().Get()
			uncertain = uncertain || commitMayHaveApplied(err)
		}

		reportSession(transaction, operation, sessionID, err)
		if err == nil {
			return nil
		}

		if isSessionOutcome(err) {
			return sessionStepError{
				operation: operation + " for session " + sessionID.String(),
				err:       markCommitUncertain(err, uncertain),
			}
		}

		if errors.Is(err, errRetireAttemptFenced) || errors.Is(err, errRetireRebuildEnded) {
			return retireAttemptEnded(
				ctx,
				operation,
				sessionID,
				markCommitUncertain(err, uncertain),
			)
		}

		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return sessionFailure(
				ctx,
				operation,
				sessionID,
				markCommitUncertain(err, uncertain),
				attempts.attributes()...,
			)
		}

		if retryError := transaction.OnError(databaseError).Get(); retryError != nil {
			return sessionFailure(
				ctx,
				operation,
				sessionID,
				markCommitUncertain(retryError, uncertain),
				attempts.attributes()...,
			)
		}

		attempts.retried(databaseError.Code)
	}
}

func retireAttemptEnded(
	ctx context.Context,
	operation string,
	sessionID uuid.UUID,
	err error,
) error {
	slog.InfoContext(
		ctx,
		"search.session.retire_attempt_ended",
		slog.String("err", err.Error()),
		slog.String("operation", operation),
		slog.String("session_id", sessionID.String()),
	)
	return sessionStepError{operation: operation + " for session " + sessionID.String(), err: err}
}

// commitUncertainError marks a session transaction failure after a submitted
// commit that could have applied. It preserves the text of err.
type commitUncertainError struct{ err error }

func (e commitUncertainError) Error() string { return e.err.Error() }
func (e commitUncertainError) Unwrap() error { return e.err }

// commitMayHaveApplied reports whether a commit error leaves the commit result
// unknown. It returns false for a nil error and for FoundationDB errors 1020
// and 1007, which indicate that the commit did not apply. It returns true for
// every other error.
func commitMayHaveApplied(err error) bool {
	var databaseError fdb.Error
	if err == nil {
		return false
	}

	if !errors.As(err, &databaseError) {
		return true
	}

	return databaseError.Code != transactionNotCommitted && databaseError.Code != transactionTooOld
}

func markCommitUncertain(err error, uncertain bool) error {
	if uncertain {
		return commitUncertainError{err: err}
	}

	return err
}

// searchTimeoutError represents a public search read or session operation that
// ended with FoundationDB error 1031 or at its context deadline. It preserves
// the text of err and wraps [searchdomain.ErrEngineUnavailable].
type searchTimeoutError struct{ err error }

func (e searchTimeoutError) Error() string { return e.err.Error() }

func (e searchTimeoutError) Unwrap() []error {
	return []error{e.err, searchdomain.ErrEngineUnavailable}
}

// classifySearchTimeout returns err as a searchTimeoutError when err wraps
// FoundationDB error 1031 or [context.DeadlineExceeded]. It returns every other
// err unchanged. It returns [context.Canceled] unchanged because the caller
// stopped waiting.
func classifySearchTimeout(err error) error {
	var databaseError fdb.Error
	timedOut := errors.As(err, &databaseError) && databaseError.Code == transactionTimedOut
	if timedOut || errors.Is(err, context.DeadlineExceeded) {
		return searchTimeoutError{err: err}
	}

	return err
}

// sessionFailure logs and wraps one session storage failure with the
// operation name and the session ID. It logs details in the transaction
// group, which the log omits when details is empty. A failure with
// FoundationDB error 1031 or [context.DeadlineExceeded] also wraps
// [searchdomain.ErrEngineUnavailable].
func sessionFailure(
	ctx context.Context,
	operation string,
	sessionID uuid.UUID,
	err error,
	details ...slog.Attr,
) error {
	wrapped := fmt.Errorf("%s for session %s: %w", operation, sessionID, classifySearchTimeout(err))
	slog.ErrorContext(
		ctx,
		"search.session.storage_failed",
		slog.String("err", wrapped.Error()),
		slog.String("operation", operation),
		slog.String("session_id", sessionID.String()),
		slog.Any("transaction", slog.GroupValue(details...)),
	)

	return wrapped
}
