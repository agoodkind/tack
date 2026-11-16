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

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

// TestFailedRepairRecordsWhatItAppliedBeforeFailing checks audit recording
// after a partial failure (TACK-452). Each rename commits separately in FDB.
// A rerun cannot recover renamed nodes from the duplicate plan.
func TestFailedRepairRecordsWhatItAppliedBeforeFailing(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	report := partialRepairReport(orgID)
	runErr := errors.New("write renumbered node: transaction too old")

	returned := repairRecordOutcome(
		context.Background(),
		report,
		runErr,
		true,
		func(recordCtx context.Context) error {
			return recordReferenceRepair(
				recordCtx,
				outbox,
				repairAuditTestPrincipal(),
				report,
				time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
			)
		},
	)

	if returned == nil || !errors.Is(returned, runErr) {
		t.Fatalf("returned = %v, want the run's own failure for operator action", returned)
	}

	events := repairAuditRecordedEvents(t, outbox, orgID)
	if len(events) != 1 {
		t.Fatalf(
			"recorded events = %d, want the one rename committed before the failure",
			len(events),
		)
	}

	rename := repairAuditExtra(t, events[0])
	if rename.From != "APP-10" || rename.To != "APP-18" {
		t.Fatalf("recorded rename = %s to %s, want APP-10 to APP-18", rename.From, rename.To)
	}
}

// TestRepairDryRunSkipsMutationRecordsAfterFailure checks that a failed dry run does not write
// mutation events for changes it did not apply.
func TestRepairDryRunSkipsMutationRecordsAfterFailure(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	report := partialRepairReport(orgID)

	returned := repairRecordOutcome(
		context.Background(),
		report,
		errors.New("list nodes: unavailable"),
		false,
		func(recordCtx context.Context) error {
			return recordReferenceRepair(
				recordCtx,
				outbox,
				repairAuditTestPrincipal(),
				report,
				time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
			)
		},
	)
	if returned == nil {
		t.Fatal("a failed dry run must still return its error")
	}

	if events := repairAuditRecordedEvents(t, outbox, orgID); len(events) != 0 {
		t.Fatalf("recorded events = %d, want zero after a dry run", len(events))
	}
}

// TestRepairRecordOutcomePreservesExecutionAndRecordingErrors requires the result to preserve both the
// run failure and the recording failure. Operators need the run failure to
// diagnose execution and the recording failure to identify unrecorded changes.
func TestRepairRecordOutcomePreservesExecutionAndRecordingErrors(t *testing.T) {
	runErr := errors.New("allocate replacement value: transaction too old")
	recordErr := errors.New("outbox unavailable")

	returned := repairRecordOutcome(
		context.Background(),
		partialRepairReport(uuid.Nil),
		runErr,
		true,
		func(context.Context) error {
			return recordErr
		},
	)

	if !errors.Is(returned, runErr) {
		t.Fatalf("returned = %v, want the run failure preserved", returned)
	}

	if !errors.Is(returned, recordErr) {
		t.Fatalf(
			"returned = %v, want the recording failure preserved: unrecorded changes exist in the store",
			returned,
		)
	}
}

// TestRepairRecordingIgnoresCommandCancellation checks that mandatory audit writes use
// a context independent of command cancellation after an interruption or timeout.
func TestRepairRecordingIgnoresCommandCancellation(t *testing.T) {
	outbox, orgID := repairAuditOutbox(t)
	report := partialRepairReport(orgID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	returned := repairRecordOutcome(
		ctx,
		report,
		context.Canceled,
		true,
		func(recordCtx context.Context) error {
			if recordCtx.Err() != nil {
				return recordCtx.Err()
			}

			return recordReferenceRepair(
				recordCtx,
				outbox,
				repairAuditTestPrincipal(),
				report,
				time.Date(2026, time.August, 31, 1, 0, 0, 0, time.UTC),
			)
		},
	)

	if !errors.Is(returned, context.Canceled) {
		t.Fatalf("returned = %v, want the run's cancellation", returned)
	}

	if events := repairAuditRecordedEvents(t, outbox, orgID); len(events) != 1 {
		t.Fatalf(
			"recorded events = %d, want the applied rename recorded despite the canceled run",
			len(events),
		)
	}
}

// TestRepairRecordingErrorPreventsSuccess checks that a recording failure
// prevents reporting success even after the repair succeeds.
func TestRepairRecordingErrorPreventsSuccess(t *testing.T) {
	recordErr := errors.New("outbox unavailable")

	returned := repairRecordOutcome(
		context.Background(),
		partialRepairReport(uuid.Nil),
		nil,
		true,
		func(context.Context) error {
			return recordErr
		},
	)

	if !errors.Is(returned, recordErr) {
		t.Fatalf("returned = %v, want the recording failure", returned)
	}
}

// partialRepairReport records one successful rename before a second fails.
func partialRepairReport(orgID uuid.UUID) RepairReferenceReport {
	nodeID := uuid.MustParse("019dc5ed-eac1-7ab4-b86b-cebc6ce06de8")

	return RepairReferenceReport{
		Renumbered: []ReferenceRename{
			{OrgID: orgID, NodeID: nodeID, From: "APP-10", To: "APP-18"},
		},
		Counters: nil,
		Keys:     nil,
	}
}

// TestRenumberGroupReturnsTheRenamesItAppliedBeforeFailing checks that the
// result includes each rename committed in its own FDB transaction before a
// later failure. The caller must record those changes in the ledger.
func TestRenumberGroupReturnsTheRenamesItAppliedBeforeFailing(t *testing.T) {
	first := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	second := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	third := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	failure := errors.New("allocate replacement value: transaction too old")
	calls := 0

	renamed, err := renumberGroupNodes(
		[]uuid.UUID{first, second, third},
		0,
		func(nodeID uuid.UUID) (ReferenceRename, error) {
			calls++
			if nodeID == third {
				return ReferenceRename{}, failure
			}

			return ReferenceRename{NodeID: nodeID, From: "APP-1", To: "APP-9"}, nil
		},
	)

	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the rename failure", err)
	}

	if calls != 2 {
		t.Fatalf(
			"renameOne calls = %d, want 2: the retained node is skipped and the run stops at the failure",
			calls,
		)
	}

	if len(renamed) != 1 || renamed[0].NodeID != second {
		t.Fatalf("renamed = %+v, want the one rename committed before the failure", renamed)
	}
}

// TestRepairFaultIsRefusedOutsideATestbed checks that the fault flag requires
// the QA generator's target marker. Partial repairs are permitted only in
// disposable environments, never production.
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

	// Ordinary repairs do not require the marker when the fault flag is absent.
	factory := &cli.Factory{Cfg: &config.Config{DatagenAllowTarget: ""}}
	if err := checkRepairFaultAllowed(context.Background(), factory, 0); err != nil {
		t.Fatalf("an ordinary run was refused: %v", err)
	}
}

// TestUnrecordedLoggingKeepsEveryIdentityInBoundedBatches checks complete
// fallback logging with bounded line sizes. A rerun cannot rediscover renamed
// nodes, and a recording failure prevents writing the command report. Batching
// must preserve every identity without one log line per change.
func TestUnrecordedLoggingKeepsEveryIdentityInBoundedBatches(t *testing.T) {
	var recorded bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(
		slog.New(
			slog.NewTextHandler(
				&recorded,
				&slog.HandlerOptions{
					AddSource:   false,
					Level:       slog.LevelError,
					ReplaceAttr: nil,
				},
			),
		),
	)
	t.Cleanup(func() { slog.SetDefault(previous) })

	report := RepairReferenceReport{Renumbered: nil, Counters: nil, Keys: nil}

	for i := range 500 {
		report.Renumbered = append(
			report.Renumbered,
			ReferenceRename{
				OrgID:  uuid.Nil,
				NodeID: uuid.New(),
				From:   "APP-" + strconv.Itoa(i),
				To:     "APP-" + strconv.Itoa(1000+i),
			},
		)
	}

	logUnrecordedRepair(context.Background(), report, errors.New("outbox unavailable"))

	logged := recorded.String()
	// Check every rename, including the first and last. Sampling would omit
	// identities that a rerun cannot recover.
	for _, rename := range report.Renumbered {
		if !strings.Contains(logged, rename.NodeID.String()) {
			t.Fatalf(
				"rename %s is missing from the log; its change is unrecorded and unrecoverable",
				rename.NodeID,
			)
		}
	}

	// Five hundred identities require ten batches of fifty and one summary.
	lines := strings.Count(logged, "\n")
	if lines != 11 {
		t.Fatalf("log lines = %d, want 11: one summary plus ten batches of fifty", lines)
	}

	if !strings.Contains(logged, "batch=10") || !strings.Contains(logged, "batches=10") {
		t.Fatalf(
			"log = %s, want each line to identify its position for detecting truncation",
			logged,
		)
	}

	if !strings.Contains(logged, "total=500") {
		t.Fatalf("log = %s, want the exact total", logged)
	}
}
