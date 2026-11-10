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

// These SQLSTATE codes report server shutdown, a server crash, or server
// startup rather than a statement refusal.
const (
	adminShutdownSQLState    = "57P01"
	crashShutdownSQLState    = "57P02"
	cannotConnectNowSQLState = "57P03"
	// connectionExceptionClass is the SQLSTATE class for a lost or refused
	// connection.
	connectionExceptionClass = "08"
)

// insertOutboxEvent inserts one event and retries once through the pool when
// the connection fails. A ledger DSN with every node permits the pool to
// connect to another serving node.
//
// The ops ledger node-prepare command stops the local ledger container before
// recording its outcome. TACK-497 documented a failed outcome write on the
// stopped connection: the command exited with an error, and deployment did
// not restart the node. The retry attempts the outcome write again after a
// lost connection.
//
// The first attempt can commit before reporting a lost connection. A duplicate
// event key on retry counts as an already-written row.
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

// isConnectionLost reports whether err indicates a lost or unavailable ledger
// connection rather than a refused statement.
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
