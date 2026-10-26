package foundationdb

import (
	"context"
	"errors"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/audit"
)

// runSequenceTransactions runs apply in one transaction with
// runNodeAttempts. When that transaction times out (1031) after at least one
// attempt conflicted (1020), and ctx has not ended, it logs the retry and
// runs apply in a new transaction. A failed transaction does not commit. It
// does not change the counter or write a change event. Every other error
// returns at once, and a timeout returns the error of retryFailure. The
// caller keeps the counter lock during all transactions.
func runSequenceTransactions(
	ctx context.Context, database fdb.Database, operation string, target audit.Target, apply func(fdb.Transaction) error,
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

// logSequenceTransactionRetry logs that transaction transactionNumber of the
// counter at target timed out after a conflict and that a new transaction
// starts. Each transaction lasts up to the transaction timeout, which limits
// this record to one per timeout for each waiting writer.
func logSequenceTransactionRetry(ctx context.Context, target audit.Target, transactionNumber int, attempts *transactionAttempts) {
	slog.InfoContext(ctx, "node.sequence.transaction_retried",
		slog.String("counter", target.URI), slog.Int("transaction", transactionNumber),
		slog.Int("attempts", attempts.count), slog.String("attempt_error_codes", attempts.codeList()))
}
