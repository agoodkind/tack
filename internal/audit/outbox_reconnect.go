package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Server-side SQLSTATE codes that mean the connection ended rather than the
// statement being refused: the server shut down, crashed, or is starting.
const (
	adminShutdownSQLState    = "57P01"
	crashShutdownSQLState    = "57P02"
	cannotConnectNowSQLState = "57P03"
	// connectionExceptionClass is the SQLSTATE class for a lost or refused
	// connection.
	connectionExceptionClass = "08"
)

// insertOutboxEvent retries an insert once on a fresh connection after a
// connection failure. The ledger DSN lists every node for connection recovery.
// The ops ledger node-prepare command stops the local ledger container before
// recording its outcome. It requires this retry to record the outcome after
// stopping the connected node (TACK-497).
//
// The first attempt can commit before reporting a connection failure. The
// retry accepts a duplicate key because the first attempt stored the row.
func (o *PoolOutbox) insertOutboxEvent(ctx context.Context, event Event, eventJSON []byte) error {
	_, err := o.pool.Exec(ctx, outboxInsertStatement, event.EventID, eventJSON)
	if err == nil {
		return nil
	}

	if !isConnectionLost(err) {
		slog.ErrorContext(
			ctx,
			"audit.outbox.insert_failed",
			slog.String("event_id", event.EventID.String()),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("insert outbox event: %w", err)
	}

	slog.WarnContext(
		ctx,
		"audit.outbox.write_reconnecting",
		slog.String("event_id", event.EventID.String()),
		slog.String("err", err.Error()),
	)
	_, retryErr := o.pool.Exec(ctx, outboxInsertStatement, event.EventID, eventJSON)
	if retryErr == nil || isOutboxDuplicate(retryErr) {
		return nil
	}

	slog.ErrorContext(
		ctx,
		"audit.outbox.reconnect_insert_failed",
		slog.String("event_id", event.EventID.String()),
		slog.String("err", retryErr.Error()),
	)

	return fmt.Errorf("insert outbox event after the connection was lost: %w", retryErr)
}

// outboxInsertStatement writes one event row to the operator outbox.
const outboxInsertStatement = `
	INSERT INTO public.ops_outbox (event_id, event)
	VALUES ($1, $2)
`

// isConnectionLost reports whether err means the connection to the ledger went
// away, as opposed to the database refusing the statement.
func isConnectionLost(err error) bool {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case adminShutdownSQLState, crashShutdownSQLState, cannotConnectNowSQLState:
			return true
		}

		return strings.HasPrefix(postgresError.Code, connectionExceptionClass)
	}

	var connectError *pgconn.ConnectError
	if errors.As(err, &connectError) {
		return true
	}

	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}

	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
