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

// TestWriteOutboxIfAbsentUnderInsertOnlyRole checks idempotent outbox writes
// with only INSERT privilege. ON CONFLICT would require SELECT while resolving
// the conflicting row, violating the operator privilege split.
func TestWriteOutboxIfAbsentUnderInsertOnlyRole(t *testing.T) {
	dsn := testenv.Ledger(t)
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}

	t.Cleanup(owner.Close)

	restricted, err := pgxpool.New(ctx, insertOnlyOutboxDSN(ctx, t, owner, dsn))
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
		t.Fatal("second write reported a duplicate insert")
	}

	var rows int
	if err := owner.QueryRow(
		ctx,
		`SELECT count(*) FROM public.ops_outbox WHERE event_id = $1`,
		event.EventID,
	).Scan(
		&rows,
	); err != nil {
		t.Fatalf("count rows: %v", err)
	}

	if rows != 1 {
		t.Fatalf("outbox rows = %d, want 1", rows)
	}
}

// insertOnlyOutboxDSN creates an INSERT-only login for the outbox and returns
// its connection string. Each run generates a credential without logging it.
func insertOnlyOutboxDSN(
	ctx context.Context,
	t *testing.T,
	owner *pgxpool.Pool,
	ownerDSN string,
) string {
	t.Helper()
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatalf("generate role credential: %v", err)
	}

	credential := hex.EncodeToString(entropy)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate role suffix: %v", err)
	}

	// Roles span the engine. Use a per-run suffix to avoid earlier test roles.
	role := "outbox_insert_only_test_" + hex.EncodeToString(suffix)

	if _, err := owner.Exec(
		ctx,
		`CREATE ROLE `+role+` LOGIN PASSWORD `+quoteLiteral(credential),
	); err != nil {
		t.Fatalf("create role: %v", err)
	}

	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `REVOKE ALL ON public.ops_outbox FROM `+role)
		_, _ = owner.Exec(ctx, `DROP ROLE IF EXISTS `+role)
	})
	if _, err := owner.Exec(ctx, `REVOKE ALL ON public.ops_outbox FROM `+role); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := owner.Exec(ctx, `GRANT INSERT ON public.ops_outbox TO `+role); err != nil {
		t.Fatalf("grant insert: %v", err)
	}

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
