package audit

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCanonicalJSONIsDeterministic(t *testing.T) {
	a := map[string]any{"b": 2, "a": 1, "nested": map[string]any{"y": []any{3, 2, 1}, "x": true}}
	b := map[string]any{"nested": map[string]any{"x": true, "y": []any{3, 2, 1}}, "a": 1, "b": 2}

	ca, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON a: %v", err)
	}
	cb, err := canonicalJSON(b)
	if err != nil {
		t.Fatalf("canonicalJSON b: %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("equal logical inputs produced different canonical bytes:\n a=%s\n b=%s", ca, cb)
	}
}

func TestHashRowChainsPreviousHash(t *testing.T) {
	prev := []byte("seed")
	h1, err := hashRow(prev, map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashRow(prev, map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1, h2) {
		t.Errorf("hashRow not deterministic")
	}
	h3, err := hashRow(append(prev, 'x'), map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(h1, h3) {
		t.Errorf("hashRow ignored prevHash")
	}
}

func TestShardOfStableAndBucketed(t *testing.T) {
	a := uuid.MustParse("019dd220-440e-729a-a442-281aaf73ca30")
	e := uuid.MustParse("019dd221-440e-729a-a442-281aaf73ca30")
	got := shardOf(a, e)
	if shardOf(a, e) != got {
		t.Errorf("shardOf not stable")
	}
	if got < 0 || got > 255 {
		t.Errorf("shardOf out of [0,255]: %d", got)
	}
}

// TestHashRowVersion3HashesStoredPrecision pins the TACK-445 contract: the
// version 3 hash covers the timestamp at the microsecond precision the
// database stores, so an event hashed with a nanosecond OccurredAt and the
// same event re-read at stored precision produce the same digest. Version 2
// is shown precision-sensitive on the same pair, which is exactly why its
// rows can only be recomputed by searching the lost nanosecond remainder.
func TestHashRowVersion3HashesStoredPrecision(t *testing.T) {
	base := rowHashInput{
		Event: Event{
			EventID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
			Actor:   Actor{Type: ActorOperator, ID: uuid.MustParse("019dd227-440e-729a-a442-281aaf73ca30")},
			Entity:  Entity{Type: "system", ID: uuid.MustParse("019dd228-440e-729a-a442-281aaf73ca30")},
			Context: EventContext{OrgID: uuid.MustParse("019dd229-440e-729a-a442-281aaf73ca30")},
			Verb:    "ops.test", Outcome: OutcomeOK,
			OccurredAt: time.Date(2026, 8, 10, 4, 17, 1, 59766789, time.UTC),
		},
		EventID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
		Shard:   9, Seq: 1, PIIRef: uuid.Nil,
		ContextJSON: []byte("{}"), DeltaJSON: []byte("null"), Version: auditHashVersion3,
	}
	nanoHash, err := hashRowForEvent(base)
	if err != nil {
		t.Fatal(err)
	}
	stored := base
	stored.Event.OccurredAt = base.Event.OccurredAt.Truncate(time.Microsecond)
	storedHash, err := hashRowForEvent(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nanoHash, storedHash) {
		t.Fatal("version 3 hash differs between nanosecond and stored-precision timestamps; verification cannot recompute it")
	}

	nanoV2 := base
	nanoV2.Version = auditHashVersion2
	storedV2 := stored
	storedV2.Version = auditHashVersion2
	nanoV2Hash, err := hashRowForEvent(nanoV2)
	if err != nil {
		t.Fatal(err)
	}
	storedV2Hash, err := hashRowForEvent(storedV2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(nanoV2Hash, storedV2Hash) {
		t.Fatal("version 2 hash unexpectedly precision-insensitive; the legacy candidate search would be unnecessary")
	}
}

func TestHashRowVersion1MatchesStoredDigest(t *testing.T) {
	input := rowHashInput{
		Event: Event{
			EventID: uuid.MustParse("019dd222-440e-729a-a442-281aaf73ca30"),
			Actor:   Actor{Type: ActorUser, ID: uuid.MustParse("019dd223-440e-729a-a442-281aaf73ca30")},
			Entity:  Entity{Type: "node", ID: uuid.MustParse("019dd224-440e-729a-a442-281aaf73ca30")},
			Context: EventContext{OrgID: uuid.MustParse("019dd225-440e-729a-a442-281aaf73ca30")},
			Verb:    "node.read", OccurredAt: time.Date(2026, 8, 8, 12, 0, 0, 123456789, time.UTC),
			IdempotencyKey: "idem-1",
		},
		EventID: uuid.MustParse("019dd222-440e-729a-a442-281aaf73ca30"),
		Shard:   7, Seq: 3, PIIRef: uuid.Nil,
		ContextJSON: []byte(`{"org_id":"019dd225-440e-729a-a442-281aaf73ca30"}`),
		DeltaJSON:   []byte("null"), LastHash: []byte("previous"), Version: auditHashVersion1,
	}
	got, err := hashRowForEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	const want = "3ffd5f063b1ae28d9e1d7c6653958529ff622904fc3c03875fb810869d614708"
	if hex.EncodeToString(got) != want {
		t.Fatalf("version 1 digest = %s, want %s", hex.EncodeToString(got), want)
	}
}

func TestHashRowVersion2CoversOutcome(t *testing.T) {
	input := rowHashInput{
		Event: Event{
			EventID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
			Actor:   Actor{Type: ActorUser, ID: uuid.MustParse("019dd227-440e-729a-a442-281aaf73ca30")},
			Entity:  Entity{Type: "node", ID: uuid.MustParse("019dd228-440e-729a-a442-281aaf73ca30")},
			Context: EventContext{OrgID: uuid.MustParse("019dd229-440e-729a-a442-281aaf73ca30")},
			Verb:    "node.update", Outcome: OutcomeOK, OccurredAt: time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		},
		EventID: uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
		Shard:   9, Seq: 1, PIIRef: uuid.Nil,
		ContextJSON: []byte("{}"), DeltaJSON: []byte("null"), Version: auditHashVersion2,
	}
	okHash, err := hashRowForEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Event.Outcome = OutcomeError
	errorHash, err := hashRowForEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(okHash, errorHash) {
		t.Fatal("version 2 hash ignored outcome")
	}
}

// TestHashRowVersion1MatchesLegacyMapPayload proves the version 1 digest did
// not change when the payload moved from a map to a struct. The map below is
// the exact payload the writer built before that refactor, field for field.
// Without this, the stored-digest test only pins the new code against itself
// and every historical row could silently stop verifying.
func TestHashRowVersion1MatchesLegacyMapPayload(t *testing.T) {
	occurred := time.Date(2026, 8, 8, 12, 0, 0, 123456789, time.UTC)
	orgID := uuid.MustParse("019dd225-440e-729a-a442-281aaf73ca30")
	eventID := uuid.MustParse("019dd222-440e-729a-a442-281aaf73ca30")
	actorID := uuid.MustParse("019dd223-440e-729a-a442-281aaf73ca30")
	entityID := uuid.MustParse("019dd224-440e-729a-a442-281aaf73ca30")
	contextJSON := []byte(`{"org_id":"019dd225-440e-729a-a442-281aaf73ca30"}`)
	deltaJSON := []byte("null")
	lastHash := []byte("previous")

	legacy := map[string]any{
		"org_id":      orgID,
		"shard":       int16(7),
		"seq":         int64(3),
		"event_id":    eventID,
		"event_time":  occurred.UTC().Format(time.RFC3339Nano),
		"actor_id":    actorID,
		"actor_kind":  ActorUser,
		"action":      "node.read",
		"entity_kind": "node",
		"entity_id":   entityID,
		"pii_ref":     uuid.Nil,
		"context":     json.RawMessage(contextJSON),
		"delta":       json.RawMessage(deltaJSON),
		"idempotency": "idem-1",
	}
	want, err := hashRow(lastHash, legacy)
	if err != nil {
		t.Fatalf("legacy hash: %v", err)
	}

	got, err := hashRowForEvent(rowHashInput{
		Event: Event{
			EventID: eventID,
			Actor:   Actor{Type: ActorUser, ID: actorID},
			Entity:  Entity{Type: "node", ID: entityID},
			Context: EventContext{OrgID: orgID},
			Verb:    "node.read", OccurredAt: occurred,
			IdempotencyKey: "idem-1",
		},
		EventID: eventID,
		Shard:   7, Seq: 3, PIIRef: uuid.Nil,
		ContextJSON: contextJSON, DeltaJSON: deltaJSON,
		LastHash: lastHash, Version: auditHashVersion1,
	})
	if err != nil {
		t.Fatalf("version 1 hash: %v", err)
	}
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Fatalf("version 1 digest changed: got %s, legacy %s",
			hex.EncodeToString(got), hex.EncodeToString(want))
	}
}
