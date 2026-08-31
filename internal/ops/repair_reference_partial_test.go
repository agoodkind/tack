package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
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

// TestRepairFaultIsRefusedOutsideATestbed pins the gate on the fault flag. It
// exists to leave a repair half applied on purpose, which is a thing to do to
// a disposable environment and never to production, so it reuses the marker
// the QA generator gates its writes on.
func TestRepairFaultIsRefusedOutsideATestbed(t *testing.T) {
	for _, target := range []string{"", "prod", "production"} {
		factory := &cli.Factory{Cfg: &config.Config{DatagenAllowTarget: target}}
		if err := checkRepairFaultAllowed(context.Background(), factory, 1); err == nil {
			t.Fatalf("target %q accepted the fault flag", target)
		}
	}
	for _, target := range []string{"qa", "local"} {
		factory := &cli.Factory{Cfg: &config.Config{DatagenAllowTarget: target}}
		if err := checkRepairFaultAllowed(context.Background(), factory, 1); err != nil {
			t.Fatalf("target %q refused the fault flag: %v", target, err)
		}
	}
	// Without the flag the marker is irrelevant, so a production repair still
	// runs: the gate must not become a second condition on ordinary work.
	factory := &cli.Factory{Cfg: &config.Config{DatagenAllowTarget: ""}}
	if err := checkRepairFaultAllowed(context.Background(), factory, 0); err != nil {
		t.Fatalf("an ordinary run was refused: %v", err)
	}
}

// TestInjectedFaultStopsAfterTheGivenRenames pins the counter the testbed
// proof depends on: the run applies exactly that many renames and then fails,
// so the ledger rows it records afterwards can be counted against a number
// chosen in advance.
func TestInjectedFaultStopsAfterTheGivenRenames(t *testing.T) {
	applied := 0
	beforeRename := func() error {
		if applied >= 2 {
			return ErrRepairFaultInjected
		}
		applied++
		return nil
	}
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		uuid.MustParse("00000000-0000-0000-0000-000000000004"),
	}

	renamed, err := renumberGroupNodes(ids, 0, func(nodeID uuid.UUID) (ReferenceRename, error) {
		if faultErr := beforeRename(); faultErr != nil {
			return ReferenceRename{}, faultErr
		}
		return ReferenceRename{NodeID: nodeID, From: "APP-1", To: "APP-9"}, nil
	})

	if !errors.Is(err, ErrRepairFaultInjected) {
		t.Fatalf("err = %v, want the injected fault", err)
	}
	if len(renamed) != 2 {
		t.Fatalf("renamed = %d, want exactly the 2 renames the fault allowed", len(renamed))
	}
}

// TestUnrecordedLoggingStaysBoundedOnALargeRepair pins the cap on the fallback
// log. A full production repair carries thousands of items, and one line each
// during an outbox outage is a log storm that outlives the command's own
// deadline, so the identities are a bounded sample and the line says how many
// it left out.
func TestUnrecordedLoggingStaysBoundedOnALargeRepair(t *testing.T) {
	var recorded bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&recorded, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelError, ReplaceAttr: nil,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	report := RepairReferenceReport{Renumbered: nil, Counters: nil, Keys: nil}
	for i := range 500 {
		report.Renumbered = append(report.Renumbered, ReferenceRename{
			OrgID:  uuid.Nil,
			NodeID: uuid.New(),
			From:   "APP-" + strconv.Itoa(i),
			To:     "APP-" + strconv.Itoa(1000+i),
		})
	}

	logUnrecordedRepair(context.Background(), report, errors.New("outbox unavailable"))

	lines := strings.Count(recorded.String(), "\n")
	if lines > 4 {
		t.Fatalf("log lines = %d, want a bounded handful for a 500-item repair", lines)
	}
	if !strings.Contains(recorded.String(), "omitted=450") {
		t.Fatalf("log = %s, want it to name the 450 identities it left out", recorded.String())
	}
	if !strings.Contains(recorded.String(), "total=500") {
		t.Fatalf("log = %s, want the exact total", recorded.String())
	}
}
