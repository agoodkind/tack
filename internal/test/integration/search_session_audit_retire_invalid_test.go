//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	retireAttemptInvalid = "cleanup_attempt_invalid"
	invalidAttemptValue  = "{"
	oversizedRecordBytes = 90000
	invalidRecordCount   = 2
)

type invalidAttemptEvidence struct {
	Digest string `json:"invalid_attempt_sha256"`
	Record []byte `json:"invalid_attempt_record"`
	Key    []byte `json:"invalid_attempt_key"`
	Index  int    `json:"invalid_attempt_chunk_index"`
	Count  int    `json:"invalid_attempt_chunk_count"`
}

// TestSearchSessionAuditRetireInvalidAttemptRecord stores one record with
// 90,000 invalid bytes and another with a malformed key. One
// RetireIndexSessions call must finish retirement and delete the session.
// Each record must produce an incomplete event with the call's actor and
// operation ID, the system organization, and only a digest in its evidence.
// The test then runs the production relay and audit consumer. It requires
// audit.events_dlq rows that reassemble every stored value and contain the
// malformed key.
func TestSearchSessionAuditRetireInvalidAttemptRecord(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	index := "node-pages-invalid-attempt"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	oversized := bytes.Repeat([]byte("x"), oversizedRecordBytes)
	integration.ReplaceRawValue(t, retireAttemptKey(t, index), oversized)
	malformedKey := fdb.Key(append(bytes.Clone(fdbadapter.TestPrefixRange()), tuple.Tuple{retireAttemptFamily, index, "malformed"}.Pack()...))
	integration.ReplaceRawValue(t, malformedKey, []byte(invalidAttemptValue))

	retryContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(retryContext)
	if err != nil {
		t.Fatalf("read the retry operation: %v", err)
	}
	remaining, err := rebuilds.RetireIndexSessions(retryContext, index, retireSliceLimit)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s with invalid attempt records = %t, %v, want a finished retirement", index, remaining, err)
	}
	if _, err := store.Load(t.Context(), session.ID); !errors.Is(err, searchdomain.ErrSessionNotFound) {
		t.Fatalf("load session %s after retirement = %v, want %v", session.ID, err, searchdomain.ErrSessionNotFound)
	}
	events := invalidAttemptEvents(t, stores, mark)
	if len(events) != invalidRecordCount {
		t.Fatalf("index %s has %d invalid attempt events, want %d", index, len(events), invalidRecordCount)
	}
	expected := map[string][]byte{digestOf(oversized): oversized, digestOf([]byte(invalidAttemptValue)): []byte(invalidAttemptValue)}
	for _, event := range events {
		var evidence invalidAttemptEvidence
		if err := json.Unmarshal(event.Extra, &evidence); err != nil || evidence.Record != nil || expected[evidence.Digest] == nil {
			t.Fatalf("invalid attempt evidence = %s, %v, want only a known digest", event.Extra, err)
		}
		if event.Actor.ID != integration.TestActorID || event.Context.OperationID != operation.Context.OperationID ||
			event.Outcome != audit.OutcomeIncomplete || event.Context.OrgID != audit.SystemOrgID() {
			t.Fatalf("invalid attempt event = %+v, want an incomplete event by actor %s under operation %s", event, integration.TestActorID, operation.Context.OperationID)
		}
	}
	retainedIDs := retainedAttemptEvents(t, stores, mark)
	stack := startRuntimeAuditStack(t)
	startRetireRelay(t, stack)
	letters := stack.waitForEventDeadLetters(t, retainedIDs)
	requireRetainedRecords(t, letters, expected, malformedKey)
}

func digestOf(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func requireRetainedRecords(t *testing.T, letters map[uuid.UUID]audit.DeadLetter, expected map[string][]byte, malformedKey fdb.Key) {
	t.Helper()
	chunks := map[string][]invalidAttemptEvidence{}
	for eventID, letter := range letters {
		var event audit.Event
		if err := json.Unmarshal(letter.Payload, &event); err != nil {
			t.Fatalf("decode the dead letter of event %s: %v", eventID, err)
		}
		var evidence invalidAttemptEvidence
		if err := json.Unmarshal(event.Extra, &evidence); err != nil {
			t.Fatalf("decode the dead letter evidence of event %s: %v", eventID, err)
		}
		chunks[evidence.Digest] = append(chunks[evidence.Digest], evidence)
	}
	keySeen := false
	for digest, value := range expected {
		parts := chunks[digest]
		slices.SortFunc(parts, func(left, right invalidAttemptEvidence) int { return left.Index - right.Index })
		rebuilt := []byte{}
		for position, part := range parts {
			if part.Index != position || part.Count != len(parts) {
				t.Fatalf("retained chunk %d of %d has index %d and count %d", position, len(parts), part.Index, part.Count)
			}
			rebuilt = append(rebuilt, part.Record...)
			keySeen = keySeen || bytes.Equal(part.Key, malformedKey)
		}
		if !bytes.Equal(rebuilt, value) {
			t.Fatalf("retained record with digest %s has %d bytes, want %d equal bytes", digest, len(rebuilt), len(value))
		}
	}
	if !keySeen {
		t.Fatalf("the retained events do not contain the malformed key %x", malformedKey)
	}
}

func invalidAttemptEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte) []audit.Event {
	t.Helper()
	selected := []audit.Event{}
	for _, event := range outboxPlainEvents(t, stores, mark) {
		if event.Error != nil && event.Error.Code == retireAttemptInvalid {
			selected = append(selected, event)
		}
	}
	return selected
}

func retainedAttemptEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte) []uuid.UUID {
	t.Helper()
	selected := []uuid.UUID{}
	for _, event := range outboxPlainEvents(t, stores, mark) {
		if event.Outcome == "" && event.EventID != uuid.Nil {
			selected = append(selected, event.EventID)
		}
	}
	return selected
}
