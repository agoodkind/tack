package ops

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBBreakGlassRefusesWrites connects as the engine superuser (TACK-554).
func TestDBBreakGlassRefusesWrites(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	reason := "TACK-554 proof " + uuid.NewString()[:8]
	deps := breakGlassDeps(t, pool, ledgerDSN, "alarm@example.test", mail.Msmtprc)

	refused := []struct {
		name      string
		statement string
		absent    string
	}{
		{
			name:      "create table",
			statement: "CREATE TABLE public.tack554_refused (id int)",
			absent:    "SELECT to_regclass('public.tack554_refused') IS NULL",
		},
		{
			name: "create partition",
			statement: "CREATE TABLE audit.events_tack554_proof PARTITION OF audit.events " +
				"FOR VALUES FROM ('2031-03-03') TO ('2031-03-10')",
			absent: "SELECT to_regclass('audit.events_tack554_proof') IS NULL",
		},
		{
			name:      "alter table",
			statement: "ALTER TABLE public.users ADD COLUMN tack554_refused int",
			absent: "SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns " +
				"WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'tack554_refused')",
		},
		{
			name:      "insert",
			statement: "INSERT INTO public.tack554_rows VALUES (1)",
			absent:    "SELECT NOT EXISTS (SELECT 1 FROM public.tack554_rows)",
		},
	}
	if _, err := pool.Exec(t.Context(), "CREATE TABLE IF NOT EXISTS public.tack554_rows (id int)"); err != nil {
		t.Fatalf("create the insert target: %v", err)
	}
	t.Cleanup(func() { dropBreakGlassTestTables(t, pool) })

	for _, attempt := range refused {
		_, err := runBreakGlass(t, deps, attempt.statement, reason)
		if !errors.Is(err, errDBStatementWrites) {
			t.Fatalf("%s: err = %v, want the read-only refusal", attempt.name, err)
		}
		var unchanged bool
		if err := pool.QueryRow(t.Context(), attempt.absent).Scan(&unchanged); err != nil {
			t.Fatalf("%s: read the database after the refusal: %v", attempt.name, err)
		}
		if !unchanged {
			t.Fatalf("%s: the refused statement changed the database", attempt.name)
		}
	}

	// Each string turns read-only off before its write. 25001 is the server's
	// refusal to change the transaction mode after a query, and 42601 is its
	// refusal of several commands in one prepared statement.
	turnsReadOnlyOff := []struct {
		statement string
		sqlState  string
	}{
		{"DO $$ BEGIN SET LOCAL transaction_read_only = off; " +
			"CREATE TABLE public.tack554_refused (id int); END $$", "25001"},
		{"COMMIT; SET default_transaction_read_only = off; BEGIN; " +
			"CREATE TABLE public.tack554_refused (id int); COMMIT", "42601"},
		{"COMMIT; SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE; BEGIN; " +
			"CREATE TABLE public.tack554_refused (id int); COMMIT", "42601"},
	}
	for _, attempt := range turnsReadOnlyOff {
		_, err := runBreakGlass(t, deps, attempt.statement, reason)
		var refusal *pgconn.PgError
		if !errors.As(err, &refusal) || refusal.Code != attempt.sqlState {
			t.Fatalf("%q: err = %v, want SQLSTATE %s", attempt.statement, err, attempt.sqlState)
		}
		var absent bool
		if err := pool.QueryRow(t.Context(), refused[0].absent).Scan(&absent); err != nil || !absent {
			t.Fatalf("%q created its table: absent = %v, err = %v", attempt.statement, absent, err)
		}
	}

	rows := breakGlassRows(t, pool, reason)
	wantRows := 2 * (len(refused) + len(turnsReadOnlyOff))
	if len(rows) != wantRows {
		t.Fatalf("ledger rows = %d, want a pending row and an error row per attempt (%d)", len(rows), wantRows)
	}
	for i := 1; i < len(rows); i += 2 {
		if rows[i].Outcome != audit.OutcomeError || rows[i].Error == nil {
			t.Fatalf("ledger row %d = %+v, want the error outcome of a refused statement", i, rows[i])
		}
	}

	result, err := runBreakGlass(t, deps, "SELECT current_setting('transaction_read_only')", reason)
	if err != nil {
		t.Fatalf("a SELECT must run: %v", err)
	}
	if result.RowsReturned != 1 || *result.Rows[0][0] != "on" {
		t.Fatalf("transaction_read_only = %v, want on", result.Rows)
	}
}
