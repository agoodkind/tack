package audit

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// readerConnectFailure is the fixed text for a failed reader connection.
const readerConnectFailure = "audit: the reader did not connect"

// readerPool sanitizes errors returned by Reader queries and scans.
type readerPool struct {
	unsanitized *pgxpool.Pool
}

func (p *readerPool) Close() {
	p.unsanitized.Close()
}

func (p *readerPool) Ping(ctx context.Context) error {
	return sanitizeReaderError(p.unsanitized.Ping(ctx))
}

// Rows returns rows without wrapping them and sanitizes the query error; pgx.Rows.Err cannot return a pgconn.ConnectError after Query acquires a connection.
func (p *readerPool) Rows(rows pgx.Rows, err error) (pgx.Rows, error) {
	return rows, sanitizeReaderError(err)
}

// Scan passes the error from pgx.Row.Scan through sanitizeReaderError. pgxpool.Pool.QueryRow reports a failed connection attempt through that Scan call.
func (p *readerPool) Scan(err error) error {
	return sanitizeReaderError(err)
}

// readerConnectError represents a failed reader connection. Error prints only the fixed text and SQLSTATE. Unwrap returns the original error for [errors.Is] and [errors.As].
type readerConnectError struct {
	original error
	sqlState string
}

func (e *readerConnectError) Error() string {
	if e.sqlState == "" {
		return readerConnectFailure
	}
	return readerConnectFailure + " (SQLSTATE " + e.sqlState + ")"
}

func (e *readerConnectError) Unwrap() error {
	return e.original
}

// sanitizeReaderError replaces errors containing a pgconn.ConnectError with readerConnectError and returns other errors unchanged. It omits the user, database, address, port, and server message because a malformed connection string can place password text in the port or database.
func sanitizeReaderError(err error) error {
	var connectError *pgconn.ConnectError
	if !errors.As(err, &connectError) {
		return err
	}
	sqlState := ""
	var serverError *pgconn.PgError
	if errors.As(err, &serverError) {
		sqlState = serverError.Code
	}
	return &readerConnectError{original: err, sqlState: sqlState}
}
