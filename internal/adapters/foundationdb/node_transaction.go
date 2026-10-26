package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
)

// nodeOperationFailure logs err as node.transaction_failed with operation and
// details, and returns err prefixed with operation. It returns err unchanged
// when err was already logged.
func nodeOperationFailure(ctx context.Context, operation string, err error, details ...slog.Attr) error {
	if searchFailureWasLogged(err) {
		return err
	}
	wrapped := fmt.Errorf("%s: %w", operation, err)
	attributes := append([]slog.Attr{slog.String("err", wrapped.Error()), slog.String("operation", operation)}, details...)
	telemetry.L(ctx).LogAttrs(ctx, slog.LevelError, "node.transaction_failed", attributes...)
	return loggedSearchError{err: wrapped}
}

// runNodeMutation runs apply in one FoundationDB transaction and reuses the
// transaction for every retry. The database transaction timeout bounds all
// attempts together. The function cancels the transaction before returning
// and prefixes errors with operation. If OnError reports a transaction
// timeout, the returned error wraps domain.ErrConflict.
func runNodeMutation(ctx context.Context, database fdb.Database, operation string, apply func(fdb.Transaction) error) error {
	transaction, err := database.CreateTransaction()
	if err != nil {
		return nodeOperationFailure(ctx, operation, fmt.Errorf("create transaction: %w", err))
	}
	defer transaction.Cancel()
	attempts := newTransactionAttempts()
	for {
		if err := ctx.Err(); err != nil {
			return nodeOperationFailure(ctx, operation, fmt.Errorf("check context: %w", err))
		}
		attempts.start()
		attemptErr := apply(transaction)
		if attemptErr == nil {
			if commitErr := transaction.Commit().Get(); commitErr != nil {
				attemptErr = fmt.Errorf("commit transaction: %w", commitErr)
			}
		}
		if attemptErr == nil {
			return nil
		}
		// The retry check runs first. A logged error can wrap a retryable
		// FoundationDB error.
		var databaseErr fdb.Error
		if !errors.As(attemptErr, &databaseErr) {
			return nodeOperationFailure(ctx, operation, attemptErr)
		}
		attempts.recordError(databaseErr.Code)
		if retryErr := transaction.OnError(databaseErr).Get(); retryErr != nil {
			return retryFailure(ctx, operation, retryErr, attempts)
		}
	}
}

// transactionTimedOut identifies the FoundationDB error transaction_timed_out.
const transactionTimedOut = 1031

// retryFailure calls nodeOperationFailure to log the refused retry as
// node.transaction_failed with slog and return an error prefixed with operation.
// The error wraps retryErr. If retryErr wraps transactionTimedOut (1031),
// the error also wraps domain.ErrConflict and includes the text
// "transaction contention: the retries exceeded the transaction timeout",
// and the log record adds the attempt count, the time since the transaction
// was created, and the attempt error codes from attempts. Otherwise, the
// error includes the text "retry transaction".
func retryFailure(ctx context.Context, operation string, retryErr error, attempts *transactionAttempts) error {
	var databaseErr fdb.Error
	if errors.As(retryErr, &databaseErr) && databaseErr.Code == transactionTimedOut {
		return nodeOperationFailure(ctx, operation,
			fmt.Errorf("transaction contention: the retries exceeded the transaction timeout: %w: %w", retryErr, domain.ErrConflict),
			attempts.logAttributes()...)
	}
	return nodeOperationFailure(ctx, operation, fmt.Errorf("retry transaction: %w", retryErr))
}

func runNodeReadTransaction(ctx context.Context, database fdb.Database, operation string, read func(fdb.Transaction) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return nodeOperationFailure(ctx, operation, fmt.Errorf("check context: %w", err))
		}
		transaction, err := database.CreateTransaction()
		if err != nil {
			return nodeOperationFailure(ctx, operation, fmt.Errorf("create transaction: %w", err))
		}
		readErr := read(transaction)
		if readErr == nil {
			transaction.Cancel()
			return nil
		}
		var databaseErr fdb.Error
		if !errors.As(readErr, &databaseErr) {
			transaction.Cancel()
			return nodeOperationFailure(ctx, operation, readErr)
		}
		if retryErr := transaction.OnError(databaseErr).Get(); retryErr != nil {
			transaction.Cancel()
			return nodeOperationFailure(ctx, operation, fmt.Errorf("retry transaction: %w", retryErr))
		}
		transaction.Cancel()
	}
}
