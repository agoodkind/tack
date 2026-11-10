package integration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	// subjectMismatchReason is the rejection text for a protected value with a
	// subject other than the target subject.
	subjectMismatchReason = "does not match target subject"
	// ownerKindReason is the rejection text for an owner on a target that is
	// not a resource.
	ownerKindReason = "only a resource target can state an owner"
)

// ownerRefusal is one event that the consumer must refuse, its protected
// reference, and the text its dead letter must contain.
type ownerRefusal struct {
	event     audit.Event
	reference uuid.UUID
	reason    string
}

// TestRuntimeAuditProtectedOwner sends events through the real consumer as
// audit_writer. An event with SubjectID equal to OwnerID must store its value
// in audit.pii under that subject and pass export verification. The consumer
// must move these events to the dead-letter table without a ledger row or an
// audit.pii row: a value with SubjectID equal to Target.ID or to another ID
// when OwnerID is set, a value with another ID when OwnerID is absent, and a
// target with OwnerID that is not a resource. An edit to owner_id in an
// exported row must fail verification.
func TestRuntimeAuditProtectedOwner(t *testing.T) {
	stack := startWriterRoleStack(t)
	redactor, redactorPool := openRedactor(t, stack)
	oldest := clock.Now().UTC().Add(-time.Minute)
	orgID, ownerID, targetID, otherID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	owned := ownedTarget(targetID, ownerID)
	text := "owner value " + uuid.NewString()
	accepted, reference := ownerEvent(t, orgID, owned, ownerID, text)
	refusals := []ownerRefusal{
		newOwnerRefusal(t, orgID, owned, targetID, subjectMismatchReason),
		newOwnerRefusal(t, orgID, owned, otherID, subjectMismatchReason),
		newOwnerRefusal(t, orgID, ownedTarget(targetID, uuid.Nil), otherID, subjectMismatchReason),
	}
	for _, target := range nonResourceOwnerTargets(ownerID) {
		refusals = append(refusals, newOwnerRefusal(t, orgID, target, ownerID, ownerKindReason))
		if _, err := audit.NewChange(uuid.Must(uuid.NewV7()), target, nil, json.RawMessage(`{"name":"synthetic"}`)); err == nil || !strings.Contains(err.Error(), ownerKindReason) {
			t.Fatalf("NewChange with %s target and an owner: err = %v, want %q", target.Kind, err, ownerKindReason)
		}
	}

	stack.record(t, accepted)
	for _, refusal := range refusals {
		stack.record(t, refusal.event)
	}

	row := stack.waitForRow(t, accepted.EventID)
	if !reflect.DeepEqual(row.Context.Target, accepted.Context.Target) || row.EntityID != targetID {
		t.Fatalf("event %s target = %+v, entity = %s, want %+v, %s", accepted.EventID, row.Context.Target, row.EntityID, accepted.Context.Target, targetID)
	}
	requireProtectedValues(t, redactorPool, map[uuid.UUID]string{reference: text})
	ownerRefs, err := redactor.PIIRefsForSubject(t.Context(), orgID, ownerID)
	if err != nil || !slices.Equal(ownerRefs, []uuid.UUID{reference}) {
		t.Fatalf("PIIRefsForSubject for the owner = %v, %v, want %v", ownerRefs, err, []uuid.UUID{reference})
	}
	targetRefs, err := redactor.PIIRefsForSubject(t.Context(), orgID, targetID)
	if err != nil || len(targetRefs) != 0 {
		t.Fatalf("PIIRefsForSubject for the target ID = %v, %v, want no references", targetRefs, err)
	}

	requireOwnerRefusals(t, stack, refusals, redactorPool)
	latest := clock.Now().UTC().Add(time.Minute)
	requireExportWithout(t, stack, orgID, oldest, latest, []string{text})
	stack.requireTampersRejected(t, orgID, oldest, latest, ownerTampers(t, accepted.EventID))
}

// ownedTarget returns a resource target for targetID with ownerID as its
// owner. A nil ownerID returns a target without an owner.
func ownedTarget(targetID, ownerID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
		URI: "tack:runtime-audit/owned/" + targetID.String(), RelationType: "",
		ID: targetID, SourceID: uuid.Nil, TargetID: uuid.Nil, OwnerID: ownerID,
	}
}

// nonResourceOwnerTargets returns one node, one relationship, and one external
// target that state ownerID.
func nonResourceOwnerTargets(ownerID uuid.UUID) []audit.Target {
	node := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil, OwnerID: ownerID,
	}
	relationship := audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
		RelationType: "parent", ID: uuid.Nil, SourceID: uuid.Must(uuid.NewV7()), TargetID: uuid.Must(uuid.NewV7()), OwnerID: ownerID,
	}
	external := syntheticExternalTarget()
	external.OwnerID = ownerID
	return []audit.Target{node, relationship, external}
}

// ownerEvent returns a format 2 change event on target with one protected
// reference and the value text under subjectID.
func ownerEvent(t *testing.T, orgID uuid.UUID, target audit.Target, subjectID uuid.UUID, text string) (audit.Event, uuid.UUID) {
	t.Helper()
	reference := uuid.Must(uuid.NewV7())
	event := protectedChangeEvent(t, orgID, target, reference)
	value, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("encode the protected value: %v", err)
	}
	event.Protected = []audit.ProtectedValue{{Ref: reference, SubjectID: subjectID, Value: value}}
	return event, reference
}

// newOwnerRefusal returns an event on target with a value under subjectID that
// the consumer must refuse with reason.
func newOwnerRefusal(t *testing.T, orgID uuid.UUID, target audit.Target, subjectID uuid.UUID, reason string) ownerRefusal {
	t.Helper()
	event, reference := ownerEvent(t, orgID, target, subjectID, "refused value "+uuid.NewString())
	return ownerRefusal{event: event, reference: reference, reason: reason}
}

// requireOwnerRefusals requires one dead letter with the expected reason for
// each refusal, no ledger row, and no audit.pii row.
func requireOwnerRefusals(t *testing.T, stack runtimeAuditStack, refusals []ownerRefusal, redactorPool *pgxpool.Pool) {
	t.Helper()
	eventIDs := make([]uuid.UUID, 0, len(refusals))
	references := make([]uuid.UUID, 0, len(refusals))
	for _, refusal := range refusals {
		eventIDs = append(eventIDs, refusal.event.EventID)
		references = append(references, refusal.reference)
	}
	letters := stack.waitForEventDeadLetters(t, eventIDs)
	for _, refusal := range refusals {
		eventID := refusal.event.EventID
		if letter := letters[eventID]; !strings.Contains(letter.Error, eventID.String()) || !strings.Contains(letter.Error, refusal.reason) {
			t.Fatalf("dead letter error of event %s = %q, want %q", eventID, letter.Error, refusal.reason)
		}
		if _, err := stack.reader.GetByID(t.Context(), eventID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("ledger read of refused event %s = %v, want pgx.ErrNoRows", eventID, err)
		}
	}
	if stored := protectedValues(t, redactorPool, references); len(stored) != 0 {
		t.Fatalf("audit.pii rows of refused events = %v, want no rows", stored)
	}
}
