package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

type auditBackfillTestSink struct{ payload json.RawMessage }

func (s *auditBackfillTestSink) WriteJSON(_ context.Context, payload json.RawMessage) error {
	s.payload = payload
	return nil
}

func (s *auditBackfillTestSink) WriteText(context.Context, string) error { return nil }

func TestReferenceRepairBackfillRefusesEachCountMismatchBeforeWrites(t *testing.T) {
	for _, testCase := range []struct {
		name, class       string
		derived, recorded int
	}{
		{name: "renames", class: "reference renames", derived: 103, recorded: 104},
		{name: "counters", class: "counter seeds", derived: 10, recorded: 11},
		{name: "keys", class: "reference keys 2026-08-07", derived: 1619, recorded: 1620},
		{name: "seed runs", class: "seed runs", derived: 1, recorded: 2},
		{name: "seed definitions", class: "seed definitions", derived: 49, recorded: 50},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outbox := &auditBackfillTestOutbox{}
			plan := auditBackfillPlan{Classes: []auditBackfillClass{{Count: auditBackfillCount{Class: testCase.class, Derived: testCase.derived, Recorded: testCase.recorded}, Events: []audit.Event{{EventID: uuid.New()}}}}}
			err := plan.Write(context.Background(), outbox)
			if err == nil {
				t.Fatal("Write succeeded with a mismatched derivation")
			}
			for _, want := range []string{testCase.class, strconv.Itoa(testCase.derived), strconv.Itoa(testCase.recorded)} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not name %q", err, want)
				}
			}
			if len(outbox.events) != 0 {
				t.Fatalf("mismatched plan wrote %d events", len(outbox.events))
			}
		})
	}
}

func TestReferenceRepairBackfillSecondWriteDoesNotDuplicate(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	if err := referenceRepairBackfillDuplicatePlan(t).Write(context.Background(), outbox); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := referenceRepairBackfillDuplicatePlan(t).Write(context.Background(), outbox); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if len(outbox.events) != 1 {
		t.Fatalf("events = %d after two writes, want 1", len(outbox.events))
	}
}

func referenceRepairBackfillDuplicatePlan(t *testing.T) auditBackfillPlan {
	t.Helper()
	event, err := reconstructionEvent(t.Context(), audit.VerbOpsRepairReferenceUniqueness, audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")}, audit.Entity{Type: "sequence_counter", ID: uuid.Nil, Identifier: "project"}, uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), "tack-429-counter:project", reconstructionExtra{Class: "counter seeds", Reconstruction: true, HistoricalTime: referenceRepairDate, Evidence: referenceRepairEvidence}, time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build reconstruction event: %v", err)
	}
	return auditBackfillPlan{Classes: []auditBackfillClass{{Count: auditBackfillCount{Class: "counter seeds", Derived: 1, Recorded: 1}, Events: []audit.Event{event}}}}
}

func referenceRepairBackfillPreviewPlan(context.Context, *config.Config, audit.OperatorPrincipal) (auditBackfillPlan, error) {
	return auditBackfillPlan{Classes: []auditBackfillClass{{Count: auditBackfillCount{Class: "counter seeds", Derived: 11, Recorded: 11}, Events: []audit.Event{{EventID: uuid.MustParse("019ff317-c5bc-75e3-a6f7-59d8f4da8277")}}}}}, nil
}

func TestReferenceRepairBackfillDryRunWritesOnlyReport(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	sink := &auditBackfillTestSink{}
	factory := &cli.Factory{Cfg: nil, In: nil, Out: nil, Err: nil}
	factory.SetOperatorIdentitySource(auditBackfillTestSource{})
	factory.SetAuditOutbox(outbox)
	err := writeReferenceRepairBackfillPreviewWith(context.Background(), factory, sink, referenceRepairBackfillPreviewPlan)
	if err != nil {
		t.Fatalf("write dry-run preview: %v", err)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("dry run wrote %d reconstruction events", len(outbox.events))
	}
	if !bytes.Contains(sink.payload, []byte("\"derived\":11")) {
		t.Fatalf("dry run report lacks counts: %s", sink.payload)
	}
}
