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

// repairAuditDSNEnv names the migrated ledger database whose outbox these
// tests write to.
const repairAuditDSNEnv = "AUDIT_CHAIN_TEST_DSN"

// repairAuditReadLimit is above the row count the gated test packages leave
// in the outbox, because the org filter runs after the read.
const repairAuditReadLimit = 10000

// repairAuditOutbox opens the ledger outbox and returns a fresh org id.
// Cleanup deletes that org's outbox rows.
func repairAuditOutbox(t *testing.T) (*audit.PoolOutbox, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv(repairAuditDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a migrated ledger DSN to run", repairAuditDSNEnv)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	outbox := audit.NewPoolOutbox(pool)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		ctx := context.Background()
		rows, readErr := outbox.ReadBatch(ctx, repairAuditReadLimit)
		if readErr != nil {
			t.Errorf("read the outbox during cleanup: %v", readErr)
		}
		for _, row := range rows {
			if row.Event.Context.OrgID != orgID {
				continue
			}
			if deleteErr := outbox.Delete(ctx, row.EventID); deleteErr != nil {
				t.Errorf("delete outbox row %s during cleanup: %v", row.EventID, deleteErr)
			}
		}
		outbox.Close()
	})
	return outbox, orgID
}

// repairAuditRecordedEvents returns the outbox events that belong to orgID.
func repairAuditRecordedEvents(t *testing.T, outbox *audit.PoolOutbox, orgID uuid.UUID) []audit.Event {
	t.Helper()
	rows, err := outbox.ReadBatch(context.Background(), repairAuditReadLimit)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	events := make([]audit.Event, 0, len(rows))
	for _, row := range rows {
		if row.Event.Context.OrgID == orgID {
			events = append(events, row.Event)
		}
	}
	return events
}

func repairAuditTestReport(orgID uuid.UUID) RepairReferenceReport {
	nodeID := uuid.MustParse("019dc5ed-eac1-7ab4-b86b-cebc6ce06de8")
	return RepairReferenceReport{
		Renumbered: []ReferenceRename{
			{OrgID: orgID, NodeID: nodeID, From: "APP-10", To: "APP-18"},
		},
		Counters: []ReferenceCounterSeed{
			{OrgID: orgID, Key: "APP-", Value: 17},
		},
		Keys: []ReferenceKeyWrite{
			{
				OrgID: orgID, NodeID: nodeID, NodeType: "issue",
				TemplateName: "reference", Encoded: "APP-18",
			},
		},
	}
}

func repairAuditTestPrincipal() audit.OperatorPrincipal {
	return audit.OperatorPrincipal{
		ID:    uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"),
		Email: "operator@example.com",
		Name:  "Operator User",
	}
}

func repairAuditExtra(t *testing.T, event audit.Event) referenceRepairExtra {
	t.Helper()
	var extra referenceRepairExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode extra %s: %v", event.Extra, err)
	}
	return extra
}

// repairAuditEventOfClass returns the one recorded event for a repair class.
func repairAuditEventOfClass(t *testing.T, events []audit.Event, class string) audit.Event {
	t.Helper()
	var found audit.Event
	matches := 0
	for _, event := range events {
		if repairAuditExtra(t, event).Class == class {
			found = event
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("events for class %q = %d, want 1", class, matches)
	}
	return found
}

// TestReferenceRepairRecordsEveryChangeItApplied is the point of TACK-448:
// the repair changes references a person may have written down, and until now
// only its own command pair reached the ledger. A history query for a
// repaired ticket returned nothing, which is the hole the 2026-08-07
// production repair left.
func TestReferenceRepairRecordsEveryChangeItApplied(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	occurredAt := time.Date(2026, time.August, 16, 21, 0, 0, 0, time.UTC)

	err := recordReferenceRepair(
		context.Background(), outbox, repairAuditTestPrincipal(),
		repairAuditTestReport(orgID), occurredAt,
	)
	if err != nil {
		t.Fatalf("record repair: %v", err)
	}
	events := repairAuditRecordedEvents(t, outbox, orgID)
	if len(events) != 3 {
		t.Fatalf("events = %d, want one per rename, counter seed, and key", len(events))
	}

	rename := repairAuditEventOfClass(t, events, "reference renames")
	if rename.Verb != string(audit.VerbNodeReferenceRename) {
		t.Fatalf("rename verb = %q, want %q", rename.Verb, audit.VerbNodeReferenceRename)
	}
	if rename.Entity.Type != "node" || rename.Entity.Identifier != "APP-18" {
		t.Fatalf("rename entity = %+v, want the renamed node holding APP-18", rename.Entity)
	}
	renameExtra := repairAuditExtra(t, rename)
	if renameExtra.From != "APP-10" || renameExtra.To != "APP-18" {
		t.Fatalf("rename extra = %+v, want APP-10 to APP-18", renameExtra)
	}

	counter := repairAuditEventOfClass(t, events, "counter seeds")
	if counter.Entity.Type != "sequence_counter" || counter.Entity.Identifier != "APP-" {
		t.Fatalf("counter entity = %+v, want the seeded counter", counter.Entity)
	}
	if got := repairAuditExtra(t, counter); got.CounterValue != 17 {
		t.Fatalf("counter extra = %+v, want the seeded value 17", got)
	}

	key := repairAuditEventOfClass(t, events, "reference keys")
	if key.Entity.Type != "node" || key.Entity.NodeType != "issue" || key.Entity.Identifier != "APP-18" {
		t.Fatalf("key entity = %+v, want the node the key was claimed for", key.Entity)
	}
	if got := repairAuditExtra(t, key); got.TemplateName != "reference" {
		t.Fatalf("key extra = %+v, want the template that rendered it", got)
	}

	for _, event := range events {
		if event.Outcome != audit.OutcomeOK {
			t.Fatalf("%s outcome = %q, want ok", event.Verb, event.Outcome)
		}
		if !event.OccurredAt.Equal(occurredAt) {
			t.Fatalf("%s occurred at %s, want the run time %s", event.Verb, event.OccurredAt, occurredAt)
		}
	}
}

// TestReferenceRepairRowsAreNotReconstructions pins that a live repair row
// has no reconstruction marker, historical time, or evidence citation,
// because a reader tells reconstructed history from live history by those
// fields.
func TestReferenceRepairRowsAreNotReconstructions(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	err := recordReferenceRepair(
		context.Background(), outbox, repairAuditTestPrincipal(),
		repairAuditTestReport(orgID), time.Date(2026, time.August, 16, 21, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("record repair: %v", err)
	}
	for _, event := range repairAuditRecordedEvents(t, outbox, orgID) {
		var fields map[string]json.RawMessage
		if unmarshalErr := json.Unmarshal(event.Extra, &fields); unmarshalErr != nil {
			t.Fatalf("decode extra: %v", unmarshalErr)
		}
		for _, absent := range []string{"reconstruction", "historical_time", "evidence"} {
			if _, present := fields[absent]; present {
				t.Fatalf("%s carries %q, so a reader would read it as reconstructed history",
					event.Verb, absent)
			}
		}
	}
}

// TestReferenceRepairSecondRunRecordsNothingNew covers the run that changes
// nothing but still seeds every counter and rewrites every key, which is what
// a second execute does. Event identity comes from the fact recorded, so the
// ledger keeps one row per fact however many times the repair runs.
func TestReferenceRepairSecondRunRecordsNothingNew(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	principal := repairAuditTestPrincipal()
	first := time.Date(2026, time.August, 16, 21, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	if err := recordReferenceRepair(
		context.Background(), outbox, principal, repairAuditTestReport(orgID), first,
	); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := recordReferenceRepair(
		context.Background(), outbox, principal, repairAuditTestReport(orgID), second,
	); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if events := repairAuditRecordedEvents(t, outbox, orgID); len(events) != 3 {
		t.Fatalf("events = %d after two runs, want 3", len(events))
	}
}

// TestReferenceRepairRefusesAnOutboxItCannotDedupe stops the repair from
// recording through a writer that would append a second copy on every run.
func TestReferenceRepairRefusesAnOutboxItCannotDedupe(t *testing.T) {
	err := recordReferenceRepair(
		context.Background(), appendOnlyRepairOutbox{}, repairAuditTestPrincipal(),
		repairAuditTestReport(uuid.Must(uuid.NewV7())), time.Date(2026, time.August, 16, 21, 0, 0, 0, time.UTC),
	)
	if err == nil {
		t.Fatal("recorded through an outbox that cannot write idempotently")
	}
}

// appendOnlyRepairOutbox writes events and offers no idempotent write.
type appendOnlyRepairOutbox struct{}

func (appendOnlyRepairOutbox) WriteOutbox(context.Context, audit.Event) error { return nil }
