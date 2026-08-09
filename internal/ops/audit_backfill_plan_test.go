package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
)

type auditBackfillTestOutbox struct {
	commandEvents []audit.Event
	events        map[uuid.UUID]audit.Event
}

func (o *auditBackfillTestOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.commandEvents = append(o.commandEvents, event)
	return nil
}

func (o *auditBackfillTestOutbox) WriteOutboxIfAbsent(
	_ context.Context,
	event audit.Event,
) (bool, error) {
	if o.events == nil {
		o.events = make(map[uuid.UUID]audit.Event)
	}
	if _, found := o.events[event.EventID]; found {
		return false, nil
	}
	o.events[event.EventID] = event
	return true, nil
}

type referenceRenameTestReader struct {
	resolution *node.NodeResolve
}

func (r referenceRenameTestReader) Resolve(context.Context, uuid.UUID) (*node.NodeResolve, error) {
	return r.resolution, nil
}

type auditBackfillTestSource struct{}

func (auditBackfillTestSource) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return audit.OperatorPrincipal{
		ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"), Email: "operator@example.com",
		Name: "Operator User", Source: "test",
	}, nil
}

func recordReferenceRenameEvidence(ctx context.Context, outbox audit.IdempotentOutboxWriter, reader referenceRenameResolver, principal audit.OperatorPrincipal, occurredAt time.Time) error {
	renames, err := loadReferenceRenameEvidence(ctx)
	if err != nil {
		return err
	}
	events := make([]audit.Event, 0, len(renames))
	for _, rename := range renames {
		event, eventErr := referenceRenameEvent(ctx, reader, principal, rename, occurredAt)
		if eventErr != nil {
			return eventErr
		}
		events = append(events, event)
	}
	return auditBackfillPlan{Classes: []auditBackfillClass{{Count: auditBackfillCount{Class: "reference renames", Derived: len(events), Recorded: len(renames)}, Events: events}}}.Write(ctx, outbox)
}

func TestReferenceRenameEvidenceHas104Records(t *testing.T) {
	var rawRecords []json.RawMessage
	if err := json.Unmarshal(referenceRenameEvidenceJSON, &rawRecords); err != nil {
		t.Fatalf("decode embedded evidence: %v", err)
	}
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	if len(renames) != len(rawRecords) || len(rawRecords) != 104 {
		t.Fatalf("loaded=%d embedded=%d, want 104", len(renames), len(rawRecords))
	}
}

func TestReferenceRenameEventMarksReconstruction(t *testing.T) {
	renames, err := loadReferenceRenameEvidence(t.Context())
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	event, err := referenceRenameEvent(context.Background(), referenceRenameTestReader{resolution: &node.NodeResolve{OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue"}}, audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")}, renames[0], time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build event: %v", err)
	}
	var extra referenceRenameExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode event extra: %v", err)
	}
	if !extra.Reconstruction || extra.Evidence != referenceRenameEvidenceCitation || extra.HistoricalTime != referenceRepairDate {
		t.Fatalf("extra = %+v, want reconstruction evidence", extra)
	}
	if event.OccurredAt.Format(time.RFC3339) != "2026-08-08T20:30:00Z" {
		t.Fatalf("event time = %s, want current append time", event.OccurredAt)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	t.Logf("ledger event: %s", encoded)
}

func TestReferenceRenameEvidenceSecondRunDoesNotDuplicate(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	reader := referenceRenameTestReader{resolution: &node.NodeResolve{OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue"}}
	principal := audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")}
	occurredAt := time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC)
	if err := recordReferenceRenameEvidence(context.Background(), outbox, reader, principal, occurredAt); err != nil {
		t.Fatalf("first reconstruction: %v", err)
	}
	if err := recordReferenceRenameEvidence(context.Background(), outbox, reader, principal, occurredAt); err != nil {
		t.Fatalf("second reconstruction: %v", err)
	}
	if len(outbox.events) != 104 {
		t.Fatalf("reconstructed events = %d, want 104 after two runs", len(outbox.events))
	}
}

func TestReferenceRenameBackfillRecordsCommandAndReconstructedEvents(t *testing.T) {
	outbox := &auditBackfillTestOutbox{}
	reader := referenceRenameTestReader{resolution: &node.NodeResolve{OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue"}}
	principal := audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"), Email: "operator@example.com", Name: "Operator User", Source: "test"}
	err := clispec.RunAudited(context.Background(), bytes.NewBuffer(nil), audit.Spec{Verb: string(audit.VerbOpsAuditReconstructReferenceRenames), Mutates: true}, auditBackfillTestSource{}, true, outbox, nil, func(ctx context.Context) error {
		return recordReferenceRenameEvidence(ctx, outbox, reader, principal, time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC))
	})
	if err != nil {
		t.Fatalf("run audited reconstruction: %v", err)
	}
	if len(outbox.commandEvents) != 2 || outbox.commandEvents[0].Outcome != audit.OutcomePending || outbox.commandEvents[1].Outcome != audit.OutcomeOK {
		t.Fatalf("command audit events = %+v, want pending then ok", outbox.commandEvents)
	}
	if len(outbox.events) != 104 {
		t.Fatalf("reconstructed events = %d, want 104", len(outbox.events))
	}
}
