package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// noRoleSetting is the value of the role setting when the session has not run SET ROLE.
const noRoleSetting = "none"

// onCheckedConnection runs call on one pooled connection and then reads the
// session role on that connection. Every read lifecycle function runs as
// SECURITY DEFINER with the privileges of ops_outbox_owner. After YugabyteDB
// retries such a call internally, for example after a deadlock or a write
// conflict, the session can keep ops_outbox_owner as current_user. An
// application query on that connection then fails with SQLSTATE 42501
// (TACK-583). When current_user differs from the session role, or the role
// read fails, onCheckedConnection logs the problem and closes the connection.
// The pool destroys a closed connection on release. The changed role belongs
// to the connection and not to the call, so onCheckedConnection always
// returns the call error unchanged, and a committed call stays reported as
// committed.
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
// role setting, or session_user when the session has not run SET ROLE. It
// logs audit.read_lifecycle.session_role_changed with both roles when they
// differ, and audit.read_lifecycle.session_role_read_failed when it cannot
// read the role.
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
