package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// TestFailedRepairRecordsWhatItAppliedBeforeFailing is TACK-452's core case.
// A run that dies partway has already written its renames to FoundationDB,
// each in its own transaction, so returning the error without recording leaves
// applied mutations with no ledger row naming them, and a rerun cannot recover
// the record because a renamed node is no longer a duplicate.
func TestFailedRepairRecordsWhatItAppliedBeforeFailing(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	report := partialRepairReport()
	runErr := errors.New("write renumbered node: transaction too old")

	returned := repairRecordOutcome(context.Background(), report, runErr, true, func(recordCtx context.Context) error {
		return recordReferenceRepair(
			recordCtx, outbox, repairAuditTestPrincipal(), report,
			time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
		)
	})

	if returned == nil || !errors.Is(returned, runErr) {
		t.Fatalf("returned = %v, want the run's own failure so the operator acts on it", returned)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("recorded events = %d, want the one rename that landed before the failure", len(outbox.events))
	}
	var recorded audit.Event
	for _, event := range outbox.events {
		recorded = event
	}
	rename := repairAuditExtra(t, recorded)
	if rename.From != "APP-10" || rename.To != "APP-18" {
		t.Fatalf("recorded rename = %s to %s, want APP-10 to APP-18", rename.From, rename.To)
	}
}

// TestDryRunFailureRecordsNothing pins that the recording is tied to work
// actually applied. A dry run changes nothing, so a dry run that fails owes
// the ledger nothing and must not write rows for renames that never happened.
func TestDryRunFailureRecordsNothing(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	report := partialRepairReport()

	returned := repairRecordOutcome(context.Background(), report, errors.New("list nodes: unavailable"), false, func(recordCtx context.Context) error {
		return recordReferenceRepair(
			recordCtx, outbox, repairAuditTestPrincipal(), report,
			time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
		)
	})

	if returned == nil {
		t.Fatal("a failed dry run must still return its error")
	}
	if len(outbox.events) != 0 {
		t.Fatalf("recorded events = %d, want none: a dry run applied nothing", len(outbox.events))
	}
}

// TestBothFailuresReachTheOperator pins what happens when the run and the
// recording both fail. Returning only the run failure would hide that applied
// changes are still unrecorded, and a rerun cannot rediscover them; returning
// only the recording failure would hide what the operator has to act on. Both
// are preserved, so either can be matched.
func TestBothFailuresReachTheOperator(t *testing.T) {
	runErr := errors.New("allocate replacement value: transaction too old")
	recordErr := errors.New("outbox unavailable")

	returned := repairRecordOutcome(context.Background(), partialRepairReport(), runErr, true, func(context.Context) error {
		return recordErr
	})

	if !errors.Is(returned, runErr) {
		t.Fatalf("returned = %v, want the run failure preserved", returned)
	}
	if !errors.Is(returned, recordErr) {
		t.Fatalf("returned = %v, want the recording failure preserved: unrecorded changes are live in the store", returned)
	}
}

// TestRecordingSurvivesACancelledRun pins the context split. A run that failed
// because the command was interrupted or timed out hands back a dead context,
// and the audit write is mandatory, so it must not inherit that cancellation:
// the record would fail exactly when the applied work most needs one.
func TestRecordingSurvivesACancelledRun(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	report := partialRepairReport()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	returned := repairRecordOutcome(ctx, report, context.Canceled, true, func(recordCtx context.Context) error {
		if recordCtx.Err() != nil {
			return recordCtx.Err()
		}
		return recordReferenceRepair(
			recordCtx, outbox, repairAuditTestPrincipal(), report,
			time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
		)
	})

	if !errors.Is(returned, context.Canceled) {
		t.Fatalf("returned = %v, want the run's cancellation", returned)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("recorded events = %d, want the applied rename recorded despite the cancelled run", len(outbox.events))
	}
}

// TestRecordFailureSurfacesWhenTheRunSucceeded pins the other half: with no
// run error to outrank it, a recording failure is the result, so a successful
// repair whose ledger write failed cannot report success.
func TestRecordFailureSurfacesWhenTheRunSucceeded(t *testing.T) {
	recordErr := errors.New("outbox unavailable")

	returned := repairRecordOutcome(context.Background(), partialRepairReport(), nil, true, func(context.Context) error {
		return recordErr
	})

	if !errors.Is(returned, recordErr) {
		t.Fatalf("returned = %v, want the recording failure", returned)
	}
}

// partialRepairReport is what a run carries when its first rename landed and
// its second failed: one applied rename, nothing else.
func partialRepairReport() RepairReferenceReport {
	orgID := uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e")
	nodeID := uuid.MustParse("019dc5ed-eac1-7ab4-b86b-cebc6ce06de8")
	return RepairReferenceReport{
		Renumbered: []ReferenceRename{
			{OrgID: orgID, NodeID: nodeID, From: "APP-10", To: "APP-18"},
		},
		Counters: nil,
		Keys:     nil,
	}
}

var _ audit.OutboxWriter = (*auditBackfillTestOutbox)(nil)

// TestRenumberGroupReturnsTheRenamesItAppliedBeforeFailing covers the group
// level, which is where the partial work was dropped first. Each rename lands
// in its own FoundationDB transaction, so the ones before a failure are live
// in the store and the caller owes each a ledger row.
func TestRenumberGroupReturnsTheRenamesItAppliedBeforeFailing(t *testing.T) {
	first := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	second := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	third := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	failure := errors.New("allocate replacement value: transaction too old")
	calls := 0

	renamed, err := renumberGroupNodes([]uuid.UUID{first, second, third}, 0, func(nodeID uuid.UUID) (ReferenceRename, error) {
		calls++
		if nodeID == third {
			return ReferenceRename{}, failure
		}
		return ReferenceRename{NodeID: nodeID, From: "APP-1", To: "APP-9"}, nil
	})

	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the rename failure", err)
	}
	if calls != 2 {
		t.Fatalf("renameOne calls = %d, want 2: the retained node is skipped and the run stops at the failure", calls)
	}
	if len(renamed) != 1 || renamed[0].NodeID != second {
		t.Fatalf("renamed = %+v, want the one rename that landed before the failure", renamed)
	}
}
