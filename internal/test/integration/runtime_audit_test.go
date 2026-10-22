package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// TestRuntimeAuditTargetRoundTrip records format 2 relationship changes and
// reads, unauthenticated refusals, and partially completed external
// operations with committed steps through the Kafka recorder and consumer.
// It compares each ledger reader row with the sent target, read evidence,
// outcome, operation identifiers, error, committed steps, and changed values.
// It verifies exports for both orgs.
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

	backup := syntheticExternalTarget()
	partial := completeEvent(orgID, userActor(), backup, audit.OperationExternal, audit.PhaseResult, audit.OutcomePartiallyCompleted)
	partial.Error = &audit.EventError{Code: "upload_failed", Message: "synthetic upload failure"}
	partial.Context.CommittedSteps = []string{"snapshot", "manifest"}

	stack.record(t, change, read, refusal, partial)
	for _, sent := range []audit.Event{change, read, refusal, partial} {
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
	if !reflect.DeepEqual(row.Context.CommittedSteps, sent.Context.CommittedSteps) {
		t.Fatalf("event %s committed steps = %v, want %v", sent.EventID, row.Context.CommittedSteps, sent.Context.CommittedSteps)
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
// outcome and a partially completed format 2 event without committed steps.
// It requires one dead-letter row per event and verifies that the ledger
// does not store either event.
func TestRuntimeAuditMissingOutcomeRejected(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	node := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	missingOutcome := completeEvent(uuid.Must(uuid.NewV7()), userActor(), node, audit.OperationRead, audit.PhaseRead, "")
	missingSteps := completeEvent(uuid.Must(uuid.NewV7()), userActor(), syntheticExternalTarget(),
		audit.OperationExternal, audit.PhaseResult, audit.OutcomePartiallyCompleted)
	missingSteps.Error = &audit.EventError{Code: "upload_failed", Message: "synthetic upload failure"}
	stack.record(t, missingOutcome, missingSteps)

	errorTexts := stack.waitForDeadLetters(t, 2)
	rejections := map[uuid.UUID]string{missingOutcome.EventID: "outcome required", missingSteps.EventID: "committed step"}
	for eventID, reason := range rejections {
		rejected := slices.ContainsFunc(errorTexts, func(text string) bool {
			return strings.Contains(text, "audit event "+eventID.String()+": ") && strings.Contains(text, reason)
		})
		if !rejected {
			t.Fatalf("dead letter errors = %q, want event %s rejected for %q", errorTexts, eventID, reason)
		}
		if _, err := stack.reader.GetByID(t.Context(), eventID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("ledger read of rejected event %s = %v, want no row", eventID, err)
		}
	}
}

// syntheticExternalTarget returns an external target with a unique
// synthetic backup reference.
func syntheticExternalTarget() audit.Target {
	return audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: "backup/synthetic-" + uuid.NewString()[:8],
		Name: "", URI: "", RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}
