package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain/node"
)

type referenceRenameTestOutbox struct {
	commandEvents []audit.Event
	events        map[uuid.UUID]audit.Event
}

func (o *referenceRenameTestOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.commandEvents = append(o.commandEvents, event)
	return nil
}

func (o *referenceRenameTestOutbox) WriteOutboxIfAbsent(
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

type referenceRenameTestSource struct{}

func (referenceRenameTestSource) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return audit.OperatorPrincipal{
		ID:     uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"),
		Email:  "operator@example.com",
		Name:   "Operator User",
		Source: "test",
	}, nil
}

func TestReferenceRenameEvidenceHas104Records(t *testing.T) {
	var rawRecords []json.RawMessage
	if err := json.Unmarshal(referenceRenameEvidenceJSON, &rawRecords); err != nil {
		t.Fatalf("decode embedded evidence: %v", err)
	}
	renames, err := loadReferenceRenameEvidence()
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	if len(renames) != len(rawRecords) {
		t.Fatalf("loaded records = %d, embedded records = %d", len(renames), len(rawRecords))
	}
	if len(rawRecords) != 104 {
		t.Fatalf("embedded evidence records = %d, want 104", len(rawRecords))
	}
}

func TestReferenceRenameEventMarksReconstruction(t *testing.T) {
	renames, err := loadReferenceRenameEvidence()
	if err != nil {
		t.Fatalf("load evidence: %v", err)
	}
	event, err := referenceRenameEvent(
		context.Background(),
		referenceRenameTestReader{resolution: &node.NodeResolve{
			OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue",
		}},
		audit.OperatorPrincipal{ID: uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd")},
		renames[0],
		time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("build event: %v", err)
	}
	var extra referenceRenameExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode event extra: %v", err)
	}
	if !extra.Reconstruction || extra.Evidence != referenceRenameEvidenceCitation {
		t.Fatalf("extra = %+v, want visible reconstruction and evidence", extra)
	}
	if event.OccurredAt.Format(time.RFC3339) != "2026-08-08T20:30:00Z" {
		t.Fatalf("event time = %s, want current append time", event.OccurredAt)
	}
	if extra.HistoricalTime != "2026-08-07" {
		t.Fatalf("historical time = %q, want 2026-08-07", extra.HistoricalTime)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	t.Logf("ledger event: %s", encoded)
}

func TestReferenceRenameEvidenceSecondRunDoesNotDuplicate(t *testing.T) {
	outbox := &referenceRenameTestOutbox{}
	reader := referenceRenameTestReader{resolution: &node.NodeResolve{
		OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue",
	}}
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
	outbox := &referenceRenameTestOutbox{}
	reader := referenceRenameTestReader{resolution: &node.NodeResolve{
		OrgID: uuid.MustParse("019ff30f-1b51-7b34-a20f-2f61b652b86e"), NodeType: "issue",
	}}
	principal := audit.OperatorPrincipal{
		ID:     uuid.MustParse("019ff315-bc5d-7a56-b12a-1a35f280c4dd"),
		Email:  "operator@example.com",
		Name:   "Operator User",
		Source: "test",
	}
	if err := clispec.RunAudited(
		context.Background(),
		bytes.NewBuffer(nil),
		audit.Spec{Verb: string(audit.VerbOpsAuditReconstructReferenceRenames), Mutates: true},
		referenceRenameTestSource{},
		true,
		outbox,
		func(ctx context.Context) error {
			return recordReferenceRenameEvidence(
				ctx,
				outbox,
				reader,
				principal,
				time.Date(2026, time.August, 8, 20, 30, 0, 0, time.UTC),
			)
		},
	); err != nil {
		t.Fatalf("run audited reconstruction: %v", err)
	}
	if len(outbox.commandEvents) != 2 {
		t.Fatalf("command audit events = %d, want intent and outcome", len(outbox.commandEvents))
	}
	if outbox.commandEvents[0].Outcome != audit.OutcomePending {
		t.Fatalf("command intent outcome = %q, want pending", outbox.commandEvents[0].Outcome)
	}
	if outbox.commandEvents[1].Outcome != audit.OutcomeOK {
		t.Fatalf("command outcome = %q, want ok", outbox.commandEvents[1].Outcome)
	}
	if len(outbox.events) != 104 {
		t.Fatalf("reconstructed events = %d, want 104", len(outbox.events))
	}
}

func TestReferenceRenameBackfillPreviewWritesNoAuditEvents(t *testing.T) {
	outbox := &referenceRenameTestOutbox{}
	var output bytes.Buffer
	factory := &cli.Factory{Out: &output, Err: &output}
	factory.SetOperatorIdentitySource(referenceRenameTestSource{})
	factory.SetAuditOutbox(outbox)
	root := &cobra.Command{Use: "tack"}
	factory.RegisterGlobalFlags(root)
	registry := clispec.NewRegistry()
	RegisterCommands(registry, factory)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	root.SetArgs([]string{"ops", "audit", "reconstruct-reference-renames"})
	if err := root.Execute(); err != nil {
		t.Fatalf("preview command: %v", err)
	}
	if len(outbox.commandEvents) != 0 || len(outbox.events) != 0 {
		t.Fatalf("preview wrote command=%d reconstruction=%d audit events, want 0",
			len(outbox.commandEvents), len(outbox.events))
	}
	if !bytes.Contains(output.Bytes(), []byte("\"count\": 104")) {
		t.Fatalf("preview did not report evidence count: %s", output.String())
	}
}
