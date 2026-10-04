package ops

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestDBBreakGlassRefusesWrites runs schema and data statements through the
// command against a real YugabyteDB as the engine superuser (TACK-554). Each
// statement fails, the named table or column does not exist afterward, and the
// ledger outbox records the attempt with an error outcome. A SELECT in the
// same session reports transaction_read_only on.
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

	// A block that turns the setting off before its write fails too. The two
	// strings after it end the transaction, turn the setting off, and write
	// in a new transaction. The simple query protocol would run each of their
	// statements; the command sends one unnamed prepared statement, and the
	// server refuses a string of several commands.
	turnsReadOnlyOff := []string{
		"DO $$ BEGIN SET LOCAL transaction_read_only = off; " +
			"CREATE TABLE public.tack554_refused (id int); END $$",
		"COMMIT; SET default_transaction_read_only = off; BEGIN; " +
			"CREATE TABLE public.tack554_refused (id int); COMMIT",
		"COMMIT; SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE; BEGIN; " +
			"CREATE TABLE public.tack554_refused (id int); COMMIT",
	}
	for _, statement := range turnsReadOnlyOff {
		if _, err := runBreakGlass(t, deps, statement, reason); err == nil {
			t.Fatalf("%q ran its write", statement)
		}
		var absent bool
		if err := pool.QueryRow(t.Context(), refused[0].absent).Scan(&absent); err != nil || !absent {
			t.Fatalf("%q created its table: absent = %v, err = %v", statement, absent, err)
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
