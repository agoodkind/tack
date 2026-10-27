package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// deadLetterReadLimit bounds one ListDeadLetters read. The limit covers the
// rows of this test and the rows that earlier tests left in the shared ledger.
const deadLetterReadLimit = 10000

// waitForEventDeadLetters reads dead-letter rows of the stack's topic through
// Reader.ListDeadLetters until each event in eventIDs has a row. It requires
// exactly one row for each event and returns the rows by event ID.
func (stack runtimeAuditStack) waitForEventDeadLetters(t *testing.T, eventIDs []uuid.UUID) map[uuid.UUID]audit.DeadLetter {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		found := stack.eventDeadLetters(t, eventIDs)
		complete := true
		for _, eventID := range eventIDs {
			if len(found[eventID]) > 1 {
				t.Fatalf("dead letters for event %s = %d, want 1", eventID, len(found[eventID]))
			}
			complete = complete && len(found[eventID]) == 1
		}
		if complete {
			letters := make(map[uuid.UUID]audit.DeadLetter, len(eventIDs))
			for _, eventID := range eventIDs {
				letters[eventID] = found[eventID][0]
			}
			return letters
		}
		if clock.Now().After(deadline) {
			t.Fatalf("dead letters for events %v on %s = %v within %s, want one each", eventIDs, stack.topic, found, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// eventDeadLetters returns the dead-letter rows of the stack's topic with a
// payload that decodes to an event in eventIDs.
func (stack runtimeAuditStack) eventDeadLetters(t *testing.T, eventIDs []uuid.UUID) map[uuid.UUID][]audit.DeadLetter {
	t.Helper()
	wanted := make(map[uuid.UUID]bool, len(eventIDs))
	for _, eventID := range eventIDs {
		wanted[eventID] = true
	}
	letters, err := stack.reader.ListDeadLetters(t.Context(), deadLetterReadLimit)
	if err != nil {
		t.Fatalf("list dead letters: %v", err)
	}
	found := make(map[uuid.UUID][]audit.DeadLetter, len(eventIDs))
	for _, letter := range letters {
		var event audit.Event
		if letter.Key.Topic != stack.topic || json.Unmarshal(letter.Payload, &event) != nil || !wanted[event.EventID] {
			continue
		}
		found[event.EventID] = append(found[event.EventID], letter)
	}
	return found
}

// requireDeadLetterPayload requires the dead-letter payload to encode the
// same event as sent.
func requireDeadLetterPayload(t *testing.T, letter audit.DeadLetter, sent audit.Event) {
	t.Helper()
	var stored audit.Event
	if err := json.Unmarshal(letter.Payload, &stored); err != nil {
		t.Fatalf("decode the dead-letter payload of event %s: %v", sent.EventID, err)
	}
	storedJSON, err := audit.MarshalEvent(stored)
	if err != nil {
		t.Fatalf("encode the stored event %s: %v", sent.EventID, err)
	}
	sentJSON, err := audit.MarshalEvent(sent)
	if err != nil {
		t.Fatalf("encode the sent event %s: %v", sent.EventID, err)
	}
	if string(storedJSON) != string(sentJSON) {
		t.Fatalf("dead-letter payload of event %s = %s, want %s", sent.EventID, storedJSON, sentJSON)
	}
}
