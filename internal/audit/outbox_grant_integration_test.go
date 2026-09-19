package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/testenv"
)

// TestWriteOutboxIfAbsentUnderInsertOnlyRole runs the idempotent outbox write
// as the kind of role an operator command authenticates as: INSERT on the
// outbox and nothing else. The reconstruction writes every one of its events
// through this path, and under ON CONFLICT it failed on the first event with
// "permission denied for table ops_outbox", because resolving a conflict
// target reads the table. The command could not have written its history on
// any environment that enforces the privilege split, production included.
func TestWriteOutboxIfAbsentUnderInsertOnlyRole(t *testing.T) {
	dsn := testenv.Ledger(t)
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	t.Cleanup(owner.Close)

	restricted, err := pgxpool.New(ctx, insertOnlyOutboxDSN(t, dsn))
	if err != nil {
		t.Fatalf("restricted pool: %v", err)
	}
	t.Cleanup(restricted.Close)

	outbox := NewPoolOutbox(restricted)
	event := outboxTestEvent("insert-only-role")
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DELETE FROM public.ops_outbox WHERE event_id = $1`, event.EventID)
	})

	written, err := outbox.WriteOutboxIfAbsent(ctx, event)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !written {
		t.Fatal("first write reported the event was already present")
	}
	rewritten, err := outbox.WriteOutboxIfAbsent(ctx, event)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if rewritten {
		t.Fatal("second write reported an insert, so a resumed run would duplicate history")
	}
	var rows int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM public.ops_outbox WHERE event_id = $1`, event.EventID,
	).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("outbox rows = %d, want 1", rows)
	}
}

// insertOnlyOutboxDSN creates a login role holding INSERT on the outbox and
// nothing else, mirroring the deployment role an operator command uses, and
// returns a connection string for it. The credential is generated per run and
// never logged.
func insertOnlyOutboxDSN(t *testing.T, ownerDSN string) string {
	t.Helper()
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatalf("generate role credential: %v", err)
	}
	credential := hex.EncodeToString(entropy)
	// Roles belong to the whole engine, which other test binaries share, so
	// the name carries a per-run suffix rather than being fixed.
	role := "outbox_insert_only_test_" + hex.EncodeToString(entropy[:4])

	testenv.ChangeRoles(t, ownerDSN,
		`CREATE ROLE `+role+` LOGIN PASSWORD `+quoteLiteral(credential),
		`REVOKE ALL ON public.ops_outbox FROM `+role,
		`GRANT INSERT ON public.ops_outbox TO `+role)
	t.Cleanup(func() {
		testenv.ChangeRoles(t, ownerDSN,
			`REVOKE ALL ON public.ops_outbox FROM `+role,
			`DROP ROLE IF EXISTS `+role)
	})
	parsed, err := url.Parse(ownerDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	parsed.User = url.UserPassword(role, credential)
	return parsed.String()
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
