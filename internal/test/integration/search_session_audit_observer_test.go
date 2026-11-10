//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

const (
	sessionTransactionByteLimit = 1_000_000
	sessionTransactionReadLimit = 3
	sessionTokensMember         = "query_tokens"
	createSessionOperation      = "create search session"
	commitPageOperation         = "commit search page"
	beginCleanupOperation       = "mark search session closing"
	cleanupSliceOperation       = "clean up search session"
)

type observedTransaction struct {
	operation string
	sessionID uuid.UUID
	bytes     int64
	reads     int
}

type sessionObservations struct {
	mutex     sync.Mutex
	committed []observedTransaction
}

func observeSessionTransactions(t *testing.T) *sessionObservations {
	t.Helper()
	observations := &sessionObservations{mutex: sync.Mutex{}, committed: nil}
	fdbadapter.SetSessionObserver(
		func(operation string, sessionID uuid.UUID, size int64, reads int) {
			observations.mutex.Lock()
			defer observations.mutex.Unlock()
			observations.committed = append(
				observations.committed,
				observedTransaction{
					operation: operation,
					sessionID: sessionID,
					bytes:     size,
					reads:     reads,
				},
			)
		},
	)
	t.Cleanup(func() { fdbadapter.SetSessionObserver(nil) })
	return observations
}

func (o *sessionObservations) requireBounded(
	t *testing.T,
	label string,
	operation string,
	sessionID uuid.UUID,
	count int,
) []observedTransaction {
	t.Helper()
	if pending := fdbadapter.PendingSessionObservations(); pending != 0 {
		t.Fatalf(
			"%s: the observer tracks %d unfinished session transactions, want 0",
			label,
			pending,
		)
	}
	o.mutex.Lock()
	committed := o.committed
	o.committed = nil
	matched := []observedTransaction{}
	for _, transaction := range committed {
		if transaction.operation == operation && transaction.sessionID == sessionID {
			matched = append(matched, transaction)
		} else {
			o.committed = append(o.committed, transaction)
		}
	}
	o.mutex.Unlock()
	for _, transaction := range matched {
		t.Logf(
			"%s: %s committed %d bytes with %d reads",
			label,
			operation,
			transaction.bytes,
			transaction.reads,
		)
		if transaction.reads < 1 || transaction.reads > sessionTransactionReadLimit ||
			transaction.bytes >= sessionTransactionByteLimit {
			t.Fatalf(
				"%s: %s committed %d bytes with %d reads, want 1 to %d reads and fewer than %d bytes",
				label,
				operation,
				transaction.bytes,
				transaction.reads,
				sessionTransactionReadLimit,
				sessionTransactionByteLimit,
			)
		}
	}
	if len(matched) != count {
		t.Fatalf(
			"%s: observed %d committed %s transactions of session %s, want %d",
			label,
			len(matched),
			operation,
			sessionID,
			count,
		)
	}
	return matched
}

func familyRecords(
	t *testing.T,
	family string,
	sessionID uuid.UUID,
	parts ...string,
) []fdb.KeyValue {
	t.Helper()
	prefix := activeTestPrefix(t)
	keyRange := prefixKeyRange(t, append(bytes.Clone(prefix), tuple.Tuple{family}.Pack()...))
	matched := []fdb.KeyValue{}
	for _, item := range readTestKeys(t, pendingFixtureDatabase(t), keyRange) {
		unpacked, err := tuple.Unpack(item.Key[len(prefix):])
		if err != nil || len(unpacked) < sessionChildPartPosition+len(parts) ||
			unpacked[sessionKeyLayouts[family].session] != sessionID.String() {
			continue
		}
		related := true
		for position, part := range parts {
			related = related && unpacked[sessionChildPartPosition+position] == part
		}
		if related {
			matched = append(matched, item)
		}
	}
	return matched
}

func addReplayRecords(t *testing.T, sessionID uuid.UUID, count int) {
	t.Helper()
	replays := familyRecords(t, sessionChildFamily, sessionID, sessionReplayPart)
	if len(replays) != 1 {
		t.Fatalf("session %s has %d replay records, want 1", sessionID, len(replays))
	}
	prefix := activeTestPrefix(t)
	unpacked, err := tuple.Unpack(replays[0].Key[len(prefix):])
	if err != nil {
		t.Fatalf("decode the replay key of session %s: %v", sessionID, err)
	}
	_, err = pendingFixtureDatabase(t).Transact(func(tr fdb.Transaction) (any, error) {
		for version := 1; version <= count; version++ {
			key := append(tuple.Tuple{}, unpacked[:sessionChildPartPosition+1]...)
			tr.Set(
				fdb.Key(append(bytes.Clone(prefix), append(key, version).Pack()...)),
				replays[0].Value,
			)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("store %d replay records of session %s: %v", count, sessionID, err)
	}
}

func sessionTokenValues(
	t *testing.T,
	stores *fdbadapter.Stores,
	mark []byte,
	sessionID uuid.UUID,
) (created, removed []json.RawMessage) {
	t.Helper()
	for _, event := range outboxPlainEvents(t, stores, mark) {
		target := event.Context.Target
		if event.Delta == nil || target == nil || target.NodeType != audit.TargetSearchSession ||
			!strings.Contains(target.URI, sessionID.String()) {
			continue
		}
		created = append(created, protectedMember(t, event, event.Delta.After)...)
		removed = append(removed, protectedMember(t, event, event.Delta.Before)...)
	}
	return created, removed
}

func protectedMember(t *testing.T, event audit.Event, record json.RawMessage) []json.RawMessage {
	t.Helper()
	if len(record) == 0 {
		return nil
	}
	member, found := deltaMembers(t, record)[sessionTokensMember]
	if !found {
		return nil
	}
	var reference audit.ProtectedReference
	if err := json.Unmarshal(member, &reference); err != nil {
		t.Fatalf(
			"event %s member %s = %s, want a protected reference: %v",
			event.EventID,
			sessionTokensMember,
			member,
			err,
		)
	}
	for _, value := range event.Protected {
		if value.Ref == reference.PIIRef {
			return []json.RawMessage{value.Value}
		}
	}
	t.Fatalf("event %s does not contain the protected value %s", event.EventID, reference.PIIRef)
	return nil
}
