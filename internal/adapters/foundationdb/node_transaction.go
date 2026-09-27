package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/telemetry"
)

func nodeOperationFailure(ctx context.Context, operation string, err error) error {
	if searchFailureWasLogged(err) {
		return err
	}
	wrapped := fmt.Errorf("%s: %w", operation, err)
	logger := telemetry.L(ctx)
	logger.ErrorContext(ctx, "node.transaction_failed", slog.String("err", wrapped.Error()), slog.String("operation", operation))
	return loggedSearchError{err: wrapped}
}

func runNodeMutation(ctx context.Context, database fdb.Database, operation string, apply func(fdb.Transaction) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return nodeOperationFailure(ctx, operation, fmt.Errorf("check context: %w", err))
		}
		transaction, err := database.CreateTransaction()
		if err != nil {
			return nodeOperationFailure(ctx, operation, fmt.Errorf("create transaction: %w", err))
		}
		attemptErr := apply(transaction)
		if attemptErr == nil {
			if commitErr := transaction.Commit().Get(); commitErr != nil {
				attemptErr = fmt.Errorf("commit transaction: %w", commitErr)
			}
		}
		if attemptErr == nil {
			transaction.Cancel()
			return nil
		}
		// The retry check runs first. A logged error can wrap a retryable
		// FoundationDB error.
		var databaseErr fdb.Error
		if !errors.As(attemptErr, &databaseErr) {
			transaction.Cancel()
			return nodeOperationFailure(ctx, operation, attemptErr)
		}
		if retryErr := transaction.OnError(databaseErr).Get(); retryErr != nil {
			transaction.Cancel()
			return nodeOperationFailure(ctx, operation, fmt.Errorf("retry transaction after %s: %w", operation, retryErr))
		}
		transaction.Cancel()
	}
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
