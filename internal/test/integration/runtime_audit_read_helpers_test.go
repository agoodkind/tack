package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// readLeaseWait is the real duration after which every lease and admission deadline
// that a test created before the wait has expired.
const readLeaseWait = audit.ReadLease + time.Second

// waitForReadLeases waits readLeaseWait in real time.
func waitForReadLeases(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(readLeaseWait)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatal("the test ended before the read leases expired")
	case <-timer.C:
	}
}

// readFixture pairs a synthetic read admission with its final completion.
type readFixture struct {
	admission  audit.ReadAdmission
	completion audit.ReadCompletion
}

// newReadFixture returns a synthetic read admission and a completion with two node targets.
func newReadFixture(t *testing.T) readFixture {
	t.Helper()
	target := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
		URI: "tack://runtime-audit/read", RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	template := completeEvent(uuid.Must(uuid.NewV7()), userActor(), target, audit.OperationRead, audit.PhaseRead, "")
	template.Context.CompletedAt = time.Time{}
	token := audit.ReadToken{
		EventID: template.EventID, Owner: audit.NewReadOwner(), Generation: 0, AdmissionDeadline: time.Time{},
	}
	targets := []audit.Target{readNodeTarget(), readNodeTarget()}
	return readFixture{
		admission:  audit.ReadAdmission{Token: token, Template: template},
		completion: completeRead(t, token, template, targets, "runtime audit read payload"),
	}
}

// readNodeTarget returns one synthetic node target.
func readNodeTarget() audit.Target {
	return audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "", RelationType: "",
		ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// completeRead returns the completed final event for template, its targets,
// and the transport digest that EncodeEvent computes.
func completeRead(t *testing.T, token audit.ReadToken, template audit.Event, targets []audit.Target, payload string) audit.ReadCompletion {
	t.Helper()
	manifestDigest, manifestParts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("compute read manifest digest: %v", err)
	}
	payloadDigest := sha256.Sum256([]byte(payload))
	final := template
	final.Outcome = audit.OutcomeCompleted
	final.Context.CompletedAt = template.Context.StartedAt.Add(time.Second)
	final.Context.Read = &audit.ReadEvidence{
		Count: uint64(len(targets)), PayloadSHA256: hex.EncodeToString(payloadDigest[:]), PayloadFormat: "application/json",
		ManifestSHA256: manifestDigest, ManifestParts: manifestParts,
	}
	encoded, err := audit.EncodeEvent(final, targets)
	if err != nil {
		t.Fatalf("encode final read %s: %v", final.EventID, err)
	}
	return audit.ReadCompletion{Token: token, Event: final, Targets: targets, Digest: encoded.TransportDigest}
}

// begin admits fixture and stores the returned token in its completion.
func (fixture *readFixture) begin(t *testing.T, store audit.ReadLifecycle) {
	t.Helper()
	token, err := store.BeginRead(t.Context(), fixture.admission)
	if err != nil {
		t.Fatalf("begin read %s: %v", fixture.admission.Token.EventID, err)
	}
	if token.Generation != audit.ReadFirstGeneration {
		t.Fatalf("read %s generation = %d, want %d", token.EventID, token.Generation, audit.ReadFirstGeneration)
	}
	fixture.completion.Token = token
}

// requireTerminal requires exactly one terminal event for eventID with
// outcome and returns it.
func requireTerminal(t *testing.T, backend *readBackend, eventID uuid.UUID, outcome audit.Outcome) audit.Event {
	t.Helper()
	events := backend.terminal(t, eventID)
	if len(events) != 1 {
		t.Fatalf("%s outbox has %d terminal events for read %s, want 1", backend.name, len(events), eventID)
	}
	if events[0].Outcome != outcome {
		t.Fatalf("%s terminal outcome of read %s = %q, want %q", backend.name, eventID, events[0].Outcome, outcome)
	}
	return events[0]
}
