package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/audit"
)

// runSequenceTransactions runs apply in a transaction with runNodeAttempts.
// If the transaction times out (1031) after an attempt conflicted (1020) and
// ctx has not ended, it logs the retry and runs apply in a new transaction.
// When an attempt changes the counter, it must write the counter and its
// change event in the same transaction. An attempt that does not change the
// counter does not write either value. FoundationDB commits a counter change
// and its event together. An error can leave a submitted commit's result
// unknown. Every other error returns immediately. A timeout that does not
// qualify for another transaction returns the error from retryFailure. The
// caller retains the counter lock across all transactions.
func runSequenceTransactions(
	ctx context.Context,
	database fdb.Database,
	operation string,
	target audit.Target,
	apply func(fdb.Transaction) error,
) error {
	for transactionNumber := 1; ; transactionNumber++ {
		err := runNodeAttempts(ctx, database, operation, apply)
		var refused *retryRefusedError
		if !errors.As(err, &refused) {
			return err
		}

		if !refused.conflictTimedOut() || ctx.Err() != nil {
			return retryFailure(ctx, operation, refused.err, refused.attempts)
		}

		logSequenceTransactionRetry(ctx, target, transactionNumber, refused.attempts)
	}
}

// logSequenceTransactionRetry logs that transactionNumber for the counter at
// target timed out after a conflict and that a new transaction will start.
// Each such record follows a transaction timeout. Each waiting writer can
// produce at most one such record per transaction timeout.
func logSequenceTransactionRetry(
	ctx context.Context,
	target audit.Target,
	transactionNumber int,
	attempts *transactionAttempts,
) {
	slog.InfoContext(
		ctx,
		"node.sequence.transaction_retried",
		slog.String("counter", target.URI),
		slog.Int("transaction", transactionNumber),
		slog.Int("attempts", attempts.count),
		slog.String("attempt_error_codes", attempts.codeList()),
		slog.Int("attempt_error_codes_omitted", attempts.omitted),
	)
}
