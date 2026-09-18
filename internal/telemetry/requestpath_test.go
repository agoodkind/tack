package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

// requestPathLine is the shape of the line the middleware logs, decoded from
// the JSON the default handler writes.
type requestPathLine struct {
	Message      string `json:"msg"`
	Path         string `json:"path"`
	LedgerReads  int64  `json:"ledger_reads"`
	LedgerWrites int64  `json:"ledger_writes"`
	SyncProduces int64  `json:"sync_produces"`
}

// captureDefaultLog routes the default logger into a buffer for one test.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

// TestRequestPathCounterLogsOneRequestsTotals serves a request through the
// middleware whose handler runs the three kinds of round trip, and reads the
// totals back from the line the middleware logs.
func TestRequestPathCounterLogsOneRequestsTotals(t *testing.T) {
	logged := captureDefaultLog(t)
	tracer := &CountingQueryTracer{Next: nil}
	handler := RequestPathCounter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT id FROM api_tokens WHERE token_hash = $1", Args: nil})
		tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "UPDATE api_tokens SET last_used = now()", Args: nil})
		tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "select distinct org_id from org_members", Args: nil})
		CountSyncProduce(ctx)
		CountSyncProduce(ctx)
		w.WriteHeader(http.StatusOK)
	}))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	response, err := http.Post(server.URL+"/mcp", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var line requestPathLine
	if err := json.Unmarshal(bytes.TrimSpace(logged.Bytes()), &line); err != nil {
		t.Fatalf("decode logged line %q: %v", logged.String(), err)
	}
	if line.Message != "request.path_counted" || line.Path != "/mcp" {
		t.Fatalf("line = %+v, want request.path_counted for /mcp", line)
	}
	if line.LedgerReads != 2 || line.LedgerWrites != 1 || line.SyncProduces != 2 {
		t.Fatalf("counts = reads %d writes %d produces %d, want 2, 1, 2",
			line.LedgerReads, line.LedgerWrites, line.SyncProduces)
	}
}

// TestCountLedgerQueryClassifiesStatements pins the read versus write split
// the criterion depends on: a SELECT and a read-only WITH are reads; a WITH
// that modifies rows, and every other statement, is a write.
func TestCountLedgerQueryClassifiesStatements(t *testing.T) {
	ctx, counts := WithRequestPath(context.Background())
	CountLedgerQuery(ctx, "SELECT 1")
	CountLedgerQuery(ctx, "  with heads as (select 1) select * from heads")
	CountLedgerQuery(ctx, "WITH moved AS (DELETE FROM a RETURNING id) INSERT INTO b SELECT id FROM moved")
	CountLedgerQuery(ctx, "INSERT INTO api_tokens (id) VALUES ($1)")
	CountLedgerQuery(ctx, "DELETE FROM org_members WHERE id = $1")
	totals := counts.Counts()
	if totals.LedgerReads != 2 || totals.LedgerWrites != 3 {
		t.Fatalf("reads %d writes %d, want 2 and 3", totals.LedgerReads, totals.LedgerWrites)
	}
}

// TestCountingOutsideARequestIsANoOp proves a query or produce on a context
// with no request attached, such as a background job, neither counts nor
// panics.
func TestCountingOutsideARequestIsANoOp(t *testing.T) {
	ctx := context.Background()
	CountLedgerQuery(ctx, "SELECT 1")
	CountSyncProduce(ctx)
	tracer := &CountingQueryTracer{Next: nil}
	if got := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1", Args: nil}); got != ctx {
		t.Fatal("a tracer with no Next must return the context it was given")
	}
	if _, ok := RequestPathFrom(ctx); ok {
		t.Fatal("no request path is attached to a bare context")
	}
}
