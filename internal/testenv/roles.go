package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// roleAttempts bounds how often one role statement is retried after the
	// engine fails it with a serialization failure.
	roleAttempts = 8
	// roleBackoffStart and roleBackoffCeiling bound the wait between attempts,
	// which doubles from the start up to the ceiling.
	roleBackoffStart   = 200 * time.Millisecond
	roleBackoffCeiling = 5 * time.Second
	// roleTimeout bounds one ChangeRoles call, including the wait for the lock
	// while another test binary migrates its database.
	roleTimeout = 15 * time.Minute
	// duplicateObject is the SQLSTATE of a CREATE ROLE whose role exists.
	duplicateObject = "42710"
)

// ChangeRoles runs role statements (CREATE, GRANT, ALTER, DROP ROLE) against
// the ledger at dsn and fails the test when one does not apply. Roles belong
// to the whole engine, not to one database, so a role change in one test
// binary conflicts with DDL in every other binary's database, and the engine
// fails the loser with SQLSTATE 40001. The statements therefore run under the
// same engine-wide lock the migrations take, and each one that still loses a
// conflict is retried with a bounded backoff.
func ChangeRoles(tb testing.TB, dsn string, statements ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), roleTimeout)
	defer cancel()
	if err := changeRoles(ctx, dsn, statements); err != nil {
		tb.Fatalf("testenv: %v", err)
	}
}

// changeRoles takes the engine lock and runs each statement with retries.
func changeRoles(ctx context.Context, dsn string, statements []string) error {
	unlock, err := lockEngine(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.roles.connect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("connect to the test ledger: %w", err)
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	for _, statement := range statements {
		if err := execRoleStatement(ctx, connection, statement); err != nil {
			return err
		}
	}
	return nil
}

// execRoleStatement runs one statement, retrying a serialization failure. A
// CREATE ROLE that failed with 40001 may still have applied, because the
// engine keeps completed DDL, so a retry that finds the role already there
// counts as applied.
func execRoleStatement(ctx context.Context, connection *pgx.Conn, statement string) error {
	backoff := roleBackoffStart
	retried := false
	for attempt := 1; ; attempt++ {
		_, err := connection.Exec(ctx, statement)
		if err == nil || (retried && hasSQLState(err, duplicateObject)) {
			return nil
		}
		if attempt == roleAttempts || !hasSQLState(err, serializationFailure) {
			slog.ErrorContext(ctx, "testenv.roles.statement_failed",
				slog.Int("attempt", attempt), slog.String("err", err.Error()))
			return fmt.Errorf("role statement failed after %d attempts: %w", attempt, err)
		}
		retried = true
		if !waitOrDone(ctx, backoff) {
			slog.ErrorContext(ctx, "testenv.roles.statement_failed", slog.String("err", err.Error()))
			return fmt.Errorf("role statement did not apply before the deadline: %w", err)
		}
		backoff = min(backoff*2, roleBackoffCeiling)
	}
}

// hasSQLState reports whether err carries the given SQLSTATE.
func hasSQLState(err error, code string) bool {
	var engineErr *pgconn.PgError
	return errors.As(err, &engineErr) && engineErr.Code == code
}

// lockEngine takes the file lock that serializes DDL on the shared engine
// among the test binaries of one machine, and returns its release.
func lockEngine(ctx context.Context) (func(), error) {
	lock := flock.New(filepath.Join(os.TempDir(), ledgerLockName))
	if err := lock.Lock(); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.lock_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("lock %s: %w", lock.Path(), err)
	}
	return func() { _ = lock.Unlock() }, nil
}

// waitOrDone waits delay and reports whether ctx is still live.
func waitOrDone(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
