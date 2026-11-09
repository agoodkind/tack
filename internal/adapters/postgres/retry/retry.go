// Package retry repeats database work after PostgreSQL aborts its transaction.
package retry

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	maximumAttempts      = 5
	firstBackoff         = 20 * time.Millisecond
	backoffMultiplier    = 2
	serializationFailure = "40001"
	deadlockDetected     = "40P01"
)

// Run calls operation again when PostgreSQL aborts its transaction.
//
// It makes at most five attempts with increasing waits and stops when ctx
// ends. Callers must limit operation to database work that the failed
// transaction rolls back. Other errors return immediately.
//
// Run retries these SQLSTATE codes:
//   - 40001 reports a serialization failure, including a rejected catalog
//     snapshot.
//   - 40P01 reports a deadlock that the database resolved by aborting
//     the transaction.
func Run(ctx context.Context, operation func(context.Context) error) error {
	backoff := firstBackoff

	for attempt := 1; ; attempt++ {
		err := operation(ctx)
		if err == nil || !retryable(err) || attempt == maximumAttempts {
			return err
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()

			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}

		slog.DebugContext(
			ctx,
			"postgresql.transaction_retrying",
			slog.Int("attempt", attempt),
			slog.Duration("wait", backoff),
			slog.String("err", err.Error()),
		)
		backoff *= backoffMultiplier
	}
}

func retryable(err error) bool {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return false
	}

	return databaseError.Code == serializationFailure || databaseError.Code == deadlockDetected
}
