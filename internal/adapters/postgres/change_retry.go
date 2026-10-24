package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// changeTxAttempts limits the transactions that runChangeTx starts for one change.
	changeTxAttempts = 5
	// changeTxFirstBackoff is the pause before the second attempt. Each later
	// pause doubles.
	changeTxFirstBackoff = 20 * time.Millisecond
	// serializationFailureSQLState is the SQLSTATE that YugabyteDB returns when
	// it aborts a transaction that conflicts with another transaction.
	serializationFailureSQLState = "40001"
	// deadlockDetectedSQLState is the SQLSTATE that YugabyteDB returns when it
	// aborts a transaction to break a deadlock.
	deadlockDetectedSQLState = "40P01"
)

// runChangeTx runs work in a new transaction on pool. It waits and retries
// when the database aborts a transaction for a conflict (SQLSTATE 40001)
// or a deadlock (SQLSTATE 40P01), up to changeTxAttempts transactions.
// Each abort rolls back the attempt's writes, including outbox events.
func runChangeTx(ctx context.Context, pool *pgxpool.Pool, work func(pgx.Tx) error) error {
	backoff := changeTxFirstBackoff
	for attempt := 1; ; attempt++ {
		err := pgx.BeginFunc(ctx, pool, work)
		if err == nil {
			return nil
		}
		if !retryableChangeErr(err) || attempt == changeTxAttempts {
			return fmt.Errorf("change transaction attempt %d: %w", attempt, err)
		}
		slog.WarnContext(ctx, "audit.change.transaction_retrying",
			slog.Int("attempt", attempt), slog.String("wait", backoff.String()), slog.String("err", err.Error()))
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, fmt.Errorf("wait to retry the change transaction: %w", ctx.Err()))
		case <-timer.C:
		}
		backoff *= 2
	}
}

// retryableChangeErr reports whether err wraps a database error with SQLSTATE
// 40001 or 40P01.
func retryableChangeErr(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == serializationFailureSQLState || postgresError.Code == deadlockDetectedSQLState
}
