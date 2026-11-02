package audit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
)

const (
	readerDSNTestSecret    = "reader-connect-secret"
	readerDSNSlashPassword = "postgres://nobody:1/" + readerDSNTestSecret + "@[::1]:1/tack"
	readerDSNTestTimeout   = 10 * time.Second
	readerDSNParseTimeout  = 2 * time.Second
	readerDSNParseFailure  = "audit: the reader connection string does not parse"
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
		{dsn: readerDSNSlashPassword, refused: false},
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

// requireReaderConnectFailure fails the test unless err contains the fixed connection failure text, omits the password, and matches pgconn.ConnectError.
func requireReaderConnectFailure(t *testing.T, path string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), readerConnectFailureText) {
		t.Errorf("%s error = %v, want the text %q", path, err, readerConnectFailureText)
		return
	}
	if strings.Contains(err.Error(), readerDSNTestSecret) {
		t.Errorf("%s error %q contains the password", path, err)
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
	reader, err := audit.NewReader(ctx, readerDSNSlashPassword)
	if err != nil {
		t.Fatalf("audit.NewReader error = %v, want nil", err)
	}
	t.Cleanup(reader.Close)
	return ctx, reader, logs
}

// TestReaderReadsOmitConnectionStringFromConnectError checks that Reader.Query and Reader.OperatorOutboxRemainder return errors and write logs containing the fixed connection failure text without the password when pgx parses a malformed connection string.
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
		t.Errorf("the logs contain the password:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), readerConnectFailureText) {
		t.Errorf("the logs omit %q:\n%s", readerConnectFailureText, logs.String())
	}
}

// TestReaderKeepsContextErrorMatching checks that errors.Is matches context.Canceled when Reader.OperatorOutboxRemainder uses a canceled context.
func TestReaderKeepsContextErrorMatching(t *testing.T) {
	ctx, reader, _ := openUnconnectableReader(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := reader.OperatorOutboxRemainder(canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reader.OperatorOutboxRemainder error = %v, want context.Canceled", err)
	}
}
