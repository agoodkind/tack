package ops

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

// deletedKeysDSNEnv names a migrated audit database. The unit tests above run
// against a fake querier, which proves the filtering rules but not the SQL:
// the action filter, the time bounds, and reading the tool back out of the
// stored context JSON are all the reader's work, and a fake cannot fail them.
const deletedKeysDSNEnv = "AUDIT_CHAIN_TEST_DSN"

func deletedKeysTestPool(t *testing.T) (*pgxpool.Pool, string, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv(deletedKeysDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated audit DSN to run", deletedKeysDSNEnv)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
		pool.Close()
	})
	return pool, dsn, orgID
}

// TestDeletedSubjectKeyEventsReadsRealDeleteRows runs the derivation through
// the production reader against a real ledger holding rows in production's
// shape. It is the step the fake querier cannot cover: the delete row's tool
// has to survive a round trip through the stored context JSON, and the action
// filter and time bounds have to select it in SQL.
func TestDeletedSubjectKeyEventsReadsRealDeleteRows(t *testing.T) {
	pool, dsn, orgID := deletedKeysTestPool(t)
	ctx := context.Background()

	// Two rows in the shape production carries: a deleted issue, whose type
	// renders a reference key, and a deleted comment, whose type does not.
	// Only the issue may be counted.
	issueRow, err := audit.WriteLegacyRow(ctx, pool, audit.LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Nil, EventID: uuid.Nil,
		Action: string(audit.VerbNodeDelete), Tool: "tack_delete_issue",
	})
	if err != nil {
		t.Fatalf("write the deleted-issue row: %v", err)
	}
	if _, err := audit.WriteLegacyRow(ctx, pool, audit.LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Nil, EventID: uuid.Nil,
		Action: string(audit.VerbNodeDelete), Tool: "tack_delete_comment",
	}); err != nil {
		t.Fatalf("write the deleted-comment row: %v", err)
	}
	// A row outside the delete verb must not be selected by the filter.
	if _, err := audit.WriteLegacyRow(ctx, pool, audit.LegacyRowInput{
		OrgID: orgID, ActorID: uuid.Must(uuid.NewV7()), EntityID: uuid.Must(uuid.NewV7()), EventID: uuid.Nil,
		Action: "", Tool: "",
	}); err != nil {
		t.Fatalf("write the tool-invocation row: %v", err)
	}

	reader, err := audit.NewReader(ctx, dsn)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(reader.Close)

	events, err := deletedSubjectKeyEvents(
		ctx, reader, deletedKeysTestTypes(), deletedKeysTestPrincipal(),
		orgID, time.Now().UTC(), referenceRepairStart,
	)
	if err != nil {
		t.Fatalf("deletedSubjectKeyEvents through the real reader: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 for the deleted issue alone", len(events))
	}

	var extra reconstructionExtra
	if err := json.Unmarshal(events[0].Extra, &extra); err != nil {
		t.Fatalf("decode extra: %v", err)
	}
	if extra.SubjectDeletionEventID != issueRow.EventID.String() {
		t.Fatalf("subject_deletion_event_id = %q, want the stored row %s",
			extra.SubjectDeletionEventID, issueRow.EventID)
	}
	if !extra.SubjectIdentityUnrecorded {
		t.Fatal("a stored row carrying the zero entity id must mark the subject identity unrecorded")
	}
	if events[0].Context.OrgID != orgID {
		t.Fatalf("event org = %s, want %s", events[0].Context.OrgID, orgID)
	}
}
