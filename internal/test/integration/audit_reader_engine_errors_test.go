package integration_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// TestAuditReaderKeepsEngineErrorMatching uses a real ledger to check that Reader.GetByID returns pgx.ErrNoRows for an unknown event, Reader.Query returns pgconn.PgError with the server message for a login without audit read access, and Reader.Query and Reader.OperatorOutboxRemainder return the fixed connection failure text and a SQLSTATE without the password for a refused login.
func TestAuditReaderKeepsEngineErrorMatching(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	if _, err := ledger.reader.GetByID(t.Context(), uuid.Must(uuid.NewV7())); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Reader.GetByID error = %v, want pgx.ErrNoRows", err)
	}
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 1,
	}
	withoutAccess, err := audit.NewReader(t.Context(), ledgerLoginDSN(t, ledger, lastChangeAppRole))
	if err != nil {
		t.Fatalf("open the reader without audit read access: %v", err)
	}
	t.Cleanup(withoutAccess.Close)
	_, denied := withoutAccess.Query(t.Context(), filter)
	var serverError *pgconn.PgError
	if !errors.As(denied, &serverError) || !strings.Contains(denied.Error(), "permission denied") {
		t.Errorf("Reader.Query error = %v, want pgconn.PgError with the server message", denied)
	}
	parsed, err := url.Parse(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.User = url.UserPassword("nobody", readerConnectSecret)
	refused, err := audit.NewReader(t.Context(), parsed.String())
	if err != nil {
		t.Fatalf("open the refused reader: %v", err)
	}
	t.Cleanup(refused.Close)
	_, queryErr := refused.Query(t.Context(), filter)
	_, remainderErr := refused.OperatorOutboxRemainder(t.Context())
	for _, login := range []error{queryErr, remainderErr} {
		if login == nil || !strings.Contains(login.Error(), readerConnectFailure) || !strings.Contains(login.Error(), "SQLSTATE") {
			t.Errorf("refused login error = %v, want %q and a SQLSTATE", login, readerConnectFailure)
			continue
		}
		if strings.Contains(login.Error(), readerConnectSecret) {
			t.Errorf("refused login error %q contains the password", login)
		}
	}
}
