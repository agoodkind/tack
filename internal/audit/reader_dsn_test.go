package audit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
)

const (
	readerDSNTestSecret     = "reader-connect-secret"
	readerDSNSecretDatabase = "postgres://[::1]:1/" + readerDSNTestSecret + "@x"
	readerDSNTestTimeout    = 10 * time.Second
	readerDSNParseTimeout   = 2 * time.Second
	readerDSNParseFailure   = "audit: the reader connection string does not parse"
	// readerConnectFailureText is the fixed text for a failed reader connection.
	readerConnectFailureText = "audit: the reader did not connect"
)

// TestNewReaderReturnsParseErrorOnlyWhenPgxCannotParse checks that audit.NewReader returns the fixed parse error only when pgx cannot parse the connection string.
func TestNewReaderReturnsParseErrorOnlyWhenPgxCannotParse(t *testing.T) {
	cases := []struct {
		dsn     string
		refused bool
	}{
		{dsn: "postgres://nobody:manifest@parts@secret@[::1:1/tack", refused: true},
		{dsn: readerDSNSecretDatabase, refused: false},
		{dsn: "postgres://h:5432/db?application_name=a@b", refused: false},
		{dsn: "postgres://h/db?application_name=a@b", refused: false},
		{dsn: "postgres://u:p@h/db?application_name=a@b", refused: false},
		{dsn: "postgres://u:p%40x@h/db", refused: false},
	}
	for _, current := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), readerDSNParseTimeout)
		reader, err := audit.NewReader(ctx, current.dsn)
		reader.Close()
		cancel()
		if current.refused && (err == nil || err.Error() != readerDSNParseFailure) {
			t.Errorf("audit.NewReader(%q) error = %v, want exactly %q", current.dsn, err, readerDSNParseFailure)
		}
		if !current.refused && err != nil {
			t.Errorf("audit.NewReader(%q) error = %v, want nil", current.dsn, err)
		}
	}
}

// requireReaderConnectFailure checks the connection failure text, secret omission, and pgconn.ConnectError matching.
func requireReaderConnectFailure(t *testing.T, path string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), readerConnectFailureText) {
		t.Errorf("%s error = %v, want the text %q", path, err, readerConnectFailureText)
		return
	}
	if strings.Contains(err.Error(), readerDSNTestSecret) {
		t.Errorf("%s error %q contains %q", path, err, readerDSNTestSecret)
	}
	var connectError *pgconn.ConnectError
	if !errors.As(err, &connectError) {
		t.Errorf("%s error %q does not match pgconn.ConnectError", path, err)
	}
}

// openUnconnectableReader opens a reader that cannot connect and returns its context, the reader, and a buffer containing its logs.
func openUnconnectableReader(t *testing.T) (context.Context, *audit.Reader, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), readerDSNTestTimeout)
	t.Cleanup(cancel)
	logs := &bytes.Buffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	reader, err := audit.NewReader(ctx, readerDSNSecretDatabase)
	if err != nil {
		t.Fatalf("audit.NewReader error = %v, want nil", err)
	}
	t.Cleanup(reader.Close)
	return ctx, reader, logs
}

// TestReaderReadsOmitConnectionStringFromConnectError checks that Reader.Query and Reader.OperatorOutboxRemainder omit the connection secret from errors and logs.
func TestReaderReadsOmitConnectionStringFromConnectError(t *testing.T) {
	ctx, reader, logs := openUnconnectableReader(t)
	now := time.Now()
	_, queryErr := reader.Query(ctx, audit.QueryFilter{
		OrgID: uuid.Must(uuid.NewV7()), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 1,
	})
	requireReaderConnectFailure(t, "Reader.Query", queryErr)
	_, remainderErr := reader.OperatorOutboxRemainder(ctx)
	requireReaderConnectFailure(t, "Reader.OperatorOutboxRemainder", remainderErr)
	if strings.Contains(logs.String(), readerDSNTestSecret) {
		t.Errorf("the logs contain %q:\n%s", readerDSNTestSecret, logs.String())
	}
	if !strings.Contains(logs.String(), readerConnectFailureText) {
		t.Errorf("the logs omit %q:\n%s", readerConnectFailureText, logs.String())
	}
}

// TestReaderKeepsConnectionRefusedMatching checks that a sanitized connection error matches syscall.ECONNREFUSED.
func TestReaderKeepsConnectionRefusedMatching(t *testing.T) {
	ctx, reader, _ := openUnconnectableReader(t)
	_, err := reader.OperatorOutboxRemainder(ctx)
	requireReaderConnectFailure(t, "Reader.OperatorOutboxRemainder", err)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("Reader.OperatorOutboxRemainder error = %v, want syscall.ECONNREFUSED", err)
	}
}
