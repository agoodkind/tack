package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// outboxPageLimit bounds the entries that one outbox read returns.
const outboxPageLimit = 500

// outboxTail returns the mark of the last FoundationDB outbox entry, or nil
// when the outbox is empty.
func outboxTail(t *testing.T, env *integration.TestEnv) []byte {
	t.Helper()
	var mark []byte
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, mark, outboxPageLimit)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return mark
		}
		mark = entries[len(entries)-1].Mark
	}
}

// outboxEventsAfter decodes every FoundationDB outbox entry after mark as one
// audit event.
func outboxEventsAfter(t *testing.T, env *integration.TestEnv, mark []byte) []audit.Event {
	t.Helper()
	events := []audit.Event{}
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, mark, outboxPageLimit)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		if len(entries) == 0 {
			return events
		}
		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil || event.Verb == "" {
				t.Fatalf("outbox entry %s is not one audit event: %v", entry.Event, err)
			}
			events = append(events, event)
		}
		mark = entries[len(entries)-1].Mark
	}
}

// changeEventsFor returns the events in events with the verb and the target
// node ID or relationship source ID targetID.
func changeEventsFor(events []audit.Event, verb audit.Verb, targetID uuid.UUID) []audit.Event {
	matched := []audit.Event{}
	for _, event := range events {
		target := event.Context.Target
		if event.Verb != string(verb) || target == nil {
			continue
		}
		if target.ID == targetID || target.SourceID == targetID {
			matched = append(matched, event)
		}
	}
	return matched
}

// requireChangeEvent requires event to be a valid format 2 change event of
// the operation operationID by the actor actorID.
func requireChangeEvent(t *testing.T, event audit.Event, operationID, actorID uuid.UUID) {
	t.Helper()
	if err := audit.ValidateEvent(event); err != nil {
		t.Fatalf("change event %s is invalid: %v", event.EventID, err)
	}
	eventContext := event.Context
	if eventContext.AuditFormatVersion != audit.AuditFormatComplete || eventContext.Phase != audit.PhaseChange {
		t.Fatalf("event %s format %d phase %q, want format 2 change", event.EventID, eventContext.AuditFormatVersion, eventContext.Phase)
	}
	if eventContext.OperationID != operationID || event.Actor.ID != actorID {
		t.Fatalf("event %s operation %s actor %s, want operation %s actor %s",
			event.EventID, eventContext.OperationID, event.Actor.ID, operationID, actorID)
	}
}
