package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// noRoleSetting is the value of the role setting when the session has not run SET ROLE.
const noRoleSetting = "none"

// ErrSessionRoleChanged reports a pooled connection with a current_user that
// differs from the role the session uses for its own statements.
var ErrSessionRoleChanged = errors.New("the ledger session role changed after a read lifecycle call")

// onCheckedConnection runs call on one pooled connection and then reads the
// session role on that connection. Every read lifecycle function runs as
// SECURITY DEFINER with the privileges of ops_outbox_owner. After YugabyteDB
// retries such a call internally, for example after a deadlock or a write
// conflict, the session can keep ops_outbox_owner as current_user. An
// application query on that connection then fails with SQLSTATE 42501
// (TACK-583). When current_user differs from the session role, or the role
// read fails, onCheckedConnection closes the connection. The pool destroys a
// closed connection on release. onCheckedConnection then returns the role
// error with the call error.
func (l *PoolReadLifecycle) onCheckedConnection(ctx context.Context, call func(connection *pgx.Conn) error) error {
	pooled, err := l.pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.acquire_failed", slog.String("err", err.Error()))
		return fmt.Errorf("acquire a ledger connection: %w", err)
	}
	defer pooled.Release()
	callErr := call(pooled.Conn())
	if pooled.Conn().IsClosed() {
		return callErr
	}
	roleErr := checkSessionRole(ctx, pooled.Conn())
	if roleErr == nil {
		return callErr
	}
	if closeErr := pooled.Conn().Close(context.WithoutCancel(ctx)); closeErr != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.connection_close_failed", slog.String("err", closeErr.Error()))
		roleErr = errors.Join(roleErr, fmt.Errorf("close the ledger connection: %w", closeErr))
	}
	return errors.Join(callErr, roleErr)
}

// checkSessionRole returns ErrSessionRoleChanged when current_user on
// connection differs from the role setting, or from session_user when the
// session has not run SET ROLE. It returns the query error when it cannot
// read the role.
func checkSessionRole(ctx context.Context, connection *pgx.Conn) error {
	var currentUser, sessionUser, roleSetting string
	err := connection.QueryRow(ctx, readStatement("session_role")).Scan(&currentUser, &sessionUser, &roleSetting)
	if err != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.session_role_read_failed", slog.String("err", err.Error()))
		return fmt.Errorf("read the ledger session role: %w", err)
	}
	expected := sessionUser
	if roleSetting != noRoleSetting {
		expected = roleSetting
	}
	if currentUser != expected {
		changed := fmt.Errorf("%w: current_user is %s and the session role is %s", ErrSessionRoleChanged, currentUser, expected)
		slog.ErrorContext(ctx, "audit.read_lifecycle.session_role_changed",
			slog.String("current_user", currentUser), slog.String("session_role", expected),
			slog.String("err", changed.Error()))
		return changed
	}
	return nil
}

// statementFailure logs the error of the read lifecycle statement name and wraps it.
func statementFailure(ctx context.Context, name string, err error) error {
	slog.ErrorContext(ctx, "audit.read_lifecycle.statement_failed",
		slog.String("statement", name), slog.String("err", err.Error()))
	return fmt.Errorf("run the %s statement: %w", name, err)
}
