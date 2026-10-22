package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// TestRuntimeAuditTargetRoundTrip records a format 2 relationship change, a
// format 2 read, and an unauthenticated refusal through the Kafka recorder
// and consumer. It compares ledger reader rows with the sent targets, read
// evidence, outcomes, operation identifiers, errors, and changed values. It
// verifies exports for both orgs.
func TestRuntimeAuditTargetRoundTrip(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	orgID := uuid.Must(uuid.NewV7())

	relationship := audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
		RelationType: "parent", ID: uuid.Must(uuid.NewV7()),
		SourceID: uuid.Must(uuid.NewV7()), TargetID: uuid.Must(uuid.NewV7()),
	}
	change := completeEvent(orgID, userActor(), relationship, audit.OperationDatabaseChange, audit.PhaseChange, audit.OutcomeCompleted)
	change.Delta = &audit.Delta{
		Before:  json.RawMessage(`{"target_id":"` + uuid.Must(uuid.NewV7()).String() + `"}`),
		After:   json.RawMessage(`{"target_id":"` + relationship.TargetID.String() + `"}`),
		Changed: []string{"target_id"},
	}

	node := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "SYN-1", Name: "Synthetic issue", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	read := completeEvent(orgID, userActor(), node, audit.OperationRead, audit.PhaseRead, audit.OutcomeCompleted)
	payloadDigest := sha256.Sum256([]byte(`{"id":"` + node.ID.String() + `"}`))
	manifestDigest := sha256.Sum256([]byte(node.ID.String()))
	read.Context.Read = &audit.ReadEvidence{
		Count: 1, PayloadSHA256: hex.EncodeToString(payloadDigest[:]), PayloadFormat: "application/json",
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]), ManifestParts: 1,
	}

	route := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: "/mcp",
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	anonymous := audit.Actor{
		Type: audit.ActorUnauthenticated, ID: uuid.Nil, Email: "", Name: "",
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
	refusal := completeEvent(audit.SystemOrgID(), anonymous, route, audit.OperationRead, audit.PhaseRead, audit.OutcomeRefused)
	refusal.Error = &audit.EventError{Code: "authentication_required", Message: "request omitted a bearer token"}

	stack.record(t, change, read, refusal)
	for _, sent := range []audit.Event{change, read, refusal} {
		row := stack.waitForRow(t, sent.EventID)
		requireRuntimeAuditRow(t, sent, row)
	}
	latest := clock.Now().UTC().Add(time.Minute)
	if err := stack.exportAndVerify(t, orgID, oldest, latest); err != nil {
		t.Fatalf("export of format 2 events failed verification: %v", err)
	}
	if err := stack.exportAndVerify(t, audit.SystemOrgID(), oldest, latest); err != nil {
		t.Fatalf("export of the unauthenticated refusal failed verification: %v", err)
	}
}

// requireRuntimeAuditRow fails the test when a stored row differs from the
// sent event.
func requireRuntimeAuditRow(t *testing.T, sent audit.Event, row audit.Row) {
	t.Helper()
	if !reflect.DeepEqual(row.Context.Target, sent.Context.Target) {
		t.Fatalf("event %s target = %+v, want %+v", sent.EventID, row.Context.Target, sent.Context.Target)
	}
	if !reflect.DeepEqual(row.Context.Read, sent.Context.Read) {
		t.Fatalf("event %s read evidence = %+v, want %+v", sent.EventID, row.Context.Read, sent.Context.Read)
	}
	if row.Outcome != sent.Outcome || row.Context.Phase != sent.Context.Phase || row.Context.OperationClass != sent.Context.OperationClass {
		t.Fatalf("event %s outcome, phase, class = %s, %s, %s", sent.EventID, row.Outcome, row.Context.Phase, row.Context.OperationClass)
	}
	if row.Context.OperationID != sent.Context.OperationID || row.Context.ParentOperationID != sent.Context.ParentOperationID {
		t.Fatalf("event %s operation ids = %s, %s", sent.EventID, row.Context.OperationID, row.Context.ParentOperationID)
	}
	if row.EntityKind != sent.Context.Target.Kind || row.EntityID != sent.Context.Target.ID || row.ActorID != sent.Actor.ID {
		t.Fatalf("event %s columns = %s %s actor %s", sent.EventID, row.EntityKind, row.EntityID, row.ActorID)
	}
	if !reflect.DeepEqual(row.Error, sent.Error) {
		t.Fatalf("event %s error = %+v, want %+v", sent.EventID, row.Error, sent.Error)
	}
	requireSameDelta(t, sent, row)
}

// requireSameDelta compares old and new values by their decoded JSON
// because the database reformats stored JSON.
func requireSameDelta(t *testing.T, sent audit.Event, row audit.Row) {
	t.Helper()
	if sent.Delta == nil || row.Delta == nil {
		if sent.Delta != row.Delta {
			t.Fatalf("event %s delta = %+v, want %+v", sent.EventID, row.Delta, sent.Delta)
		}
		return
	}
	for _, pair := range [][2]json.RawMessage{{sent.Delta.Before, row.Delta.Before}, {sent.Delta.After, row.Delta.After}} {
		var want, got map[string]string
		if err := json.Unmarshal(pair[0], &want); err != nil {
			t.Fatalf("decode sent delta: %v", err)
		}
		if err := json.Unmarshal(pair[1], &got); err != nil {
			t.Fatalf("decode stored delta: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("event %s delta value = %v, want %v", sent.EventID, got, want)
		}
	}
	if !reflect.DeepEqual(row.Delta.Changed, sent.Delta.Changed) {
		t.Fatalf("event %s changed fields = %v, want %v", sent.EventID, row.Delta.Changed, sent.Delta.Changed)
	}
}

// TestRuntimeAuditMissingOutcomeRejected sends a format 2 event without an
// outcome and requires one dead-letter row without a ledger row.
func TestRuntimeAuditMissingOutcomeRejected(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	node := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	event := completeEvent(uuid.Must(uuid.NewV7()), userActor(), node, audit.OperationRead, audit.PhaseRead, "")
	stack.record(t, event)

	errorText := stack.waitForDeadLetter(t)
	if !strings.Contains(errorText, event.EventID.String()) || !strings.Contains(errorText, "outcome required") {
		t.Fatalf("dead letter error = %q, want the missing outcome of event %s", errorText, event.EventID)
	}
	if _, err := stack.reader.GetByID(t.Context(), event.EventID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ledger read of rejected event %s = %v, want no row", event.EventID, err)
	}
}
