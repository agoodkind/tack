package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// noRoleSetting specifies the role setting before the session runs SET ROLE.
const noRoleSetting = "none"

// onCheckedConnection runs call on one pooled connection and checks that
// connection's session role afterward. Each read lifecycle function uses
// SECURITY DEFINER with ops_outbox_owner privileges.
//
// TACK-583 records a YugabyteDB failure in which an internal retry after a
// deadlock or write conflict leaves current_user as ops_outbox_owner. Later
// application queries on that connection fail with SQLSTATE 42501, which
// reports insufficient privileges.
//
// The method logs a role mismatch or failed role read and closes the affected
// connection. The pool destroys the closed connection on release. The role
// change affects the connection rather than the call result. The method
// returns the call error unchanged and reports a committed call as committed.
func (l *PoolReadLifecycle) onCheckedConnection(ctx context.Context, call func(connection *pgx.Conn) error) error {
	pooled, err := l.pool.Acquire(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.acquire_failed", slog.String("err", err.Error()))
		return fmt.Errorf("acquire a ledger connection: %w", err)
	}
	defer pooled.Release()
	callErr := call(pooled.Conn())
	if pooled.Conn().IsClosed() || sessionRoleVerified(ctx, pooled.Conn()) {
		return callErr
	}
	if closeErr := pooled.Conn().Close(context.WithoutCancel(ctx)); closeErr != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.connection_close_failed", slog.String("err", closeErr.Error()))
	}
	return callErr
}

// sessionRoleVerified reports whether current_user on connection equals the
// role setting, or session_user before SET ROLE.
//
// The function logs audit.read_lifecycle.session_role_changed with both roles
// when they differ. It logs audit.read_lifecycle.session_role_read_failed when
// it cannot read the role.
func sessionRoleVerified(ctx context.Context, connection *pgx.Conn) bool {
	var currentUser, sessionUser, roleSetting string
	err := connection.QueryRow(ctx, readStatement("session_role")).Scan(&currentUser, &sessionUser, &roleSetting)
	if err != nil {
		slog.ErrorContext(ctx, "audit.read_lifecycle.session_role_read_failed", slog.String("err", err.Error()))
		return false
	}
	expected := sessionUser
	if roleSetting != noRoleSetting {
		expected = roleSetting
	}
	if currentUser == expected {
		return true
	}
	slog.ErrorContext(ctx, "audit.read_lifecycle.session_role_changed",
		slog.String("current_user", currentUser), slog.String("session_role", expected),
		slog.String("err", "current_user differs from the session role; the connection is closed"))
	return false
}

func statementFailure(ctx context.Context, name string, err error) error {
	slog.ErrorContext(ctx, "audit.read_lifecycle.statement_failed",
		slog.String("statement", name), slog.String("err", err.Error()))
	return fmt.Errorf("run the %s statement: %w", name, err)
}
