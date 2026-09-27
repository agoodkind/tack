package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const searchTransactionTimeout = 5 * time.Second

// transactSearch runs apply and commits it, retrying every retryable
// FoundationDB error. FoundationDB enforces the transaction timeout in wall
// time. Each attempt reads the wall clock and sets the timeout to the time
// left before the context deadline, capped at searchTransactionTimeout.
func transactSearch(ctx context.Context, db fdb.Database, apply func(fdb.Transaction) error) error {
	transaction, err := db.CreateTransaction()
	if err != nil {
		return transactionFailure(ctx, "create search transaction", err)
	}
	defer transaction.Cancel()
	for {
		if err := ctx.Err(); err != nil {
			return transactionFailure(ctx, "cancel search transaction", err)
		}
		timeout := searchTransactionTimeout
		if deadline, exists := ctx.Deadline(); exists {
			timeout = max(time.Millisecond, min(timeout, deadline.Sub(clock.Now())))
		}
		if err := transaction.Options().SetTimeout(max(int64(timeout/time.Millisecond), 1)); err != nil {
			return transactionFailure(ctx, "set search transaction timeout", err)
		}
		err := apply(transaction)
		if err == nil {
			err = transaction.Commit().Get()
		}
		if err == nil {
			return nil
		}
		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return transactionFailure(ctx, "run search transaction", err)
		}
		if retryError := transaction.OnError(databaseError).Get(); retryError != nil {
			return transactionFailure(ctx, "retry search transaction", retryError)
		}
	}
}

type loggedSearchError struct{ err error }

func (e loggedSearchError) Error() string { return e.err.Error() }
func (e loggedSearchError) Unwrap() error { return e.err }

func searchFailureWasLogged(err error) bool {
	var logged loggedSearchError
	return errors.As(err, &logged)
}

// searchReadFailure wraps one failed read inside a search transaction. A
// FoundationDB error returns unlogged. The retry loop retries it or logs it.
// Every other error is logged here once.
func searchReadFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	var databaseError fdb.Error
	if errors.As(err, &databaseError) || searchFailureWasLogged(err) {
		return wrapped
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.read_failed", slog.String("err", wrapped.Error()))
	return loggedSearchError{err: wrapped}
}

// transactionFailure logs one failure once. Expected search outcomes pass
// through unchanged.
func transactionFailure(ctx context.Context, operation string, err error) error {
	if errors.Is(err, search.ErrNoWork) || errors.Is(err, search.ErrWorkChanged) || errors.Is(err, search.ErrNoServingIndex) ||
		errors.Is(err, node.ErrContentChanged) {
		return err
	}
	if searchFailureWasLogged(err) {
		return err
	}
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.transaction.failed", slog.String("err", wrapped.Error()))
	return loggedSearchError{err: wrapped}
}
