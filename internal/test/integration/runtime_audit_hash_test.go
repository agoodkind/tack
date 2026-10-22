package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// historicalHashVersion is the hash version of every row the consumer
// writes.
const historicalHashVersion = 3

func TestRuntimeAuditHistoricalHashCompatibility(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	orgID := uuid.Must(uuid.NewV7())
	completed := legacyReadEvent(orgID, audit.OutcomeOK)
	unstated := legacyReadEvent(orgID, "")
	stack.record(t, completed, unstated)

	for _, sent := range []audit.Event{completed, unstated} {
		row := stack.waitForRow(t, sent.EventID)
		if row.Outcome != audit.OutcomeOK {
			t.Fatalf("format 0 event %s outcome = %q, want %q", sent.EventID, row.Outcome, audit.OutcomeOK)
		}
		want := historicalRowHash(t, orgID, row)
		if !bytes.Equal(row.RowHash, want) {
			t.Fatalf("format 0 event %s row hash = %x, want the historical hash %x", sent.EventID, row.RowHash, want)
		}
	}
	if err := stack.exportAndVerify(t, orgID, oldest, clock.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("export of format 0 events failed verification: %v", err)
	}
}

// legacyReadEvent returns a format 0 read event in the shape current
// writers produce.
func legacyReadEvent(orgID uuid.UUID, outcome audit.Outcome) audit.Event {
	return audit.Event{
		Verb: string(audit.VerbNodeRead), EventID: uuid.Must(uuid.NewV7()), Actor: userActor(),
		Entity: audit.Entity{Type: "node", NodeType: "", ID: uuid.Must(uuid.NewV7()), Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceMCP, Tool: "tack_get_issue", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: outcome, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: nil,
	}
}

// historicalRowHash computes the version 3 row hash over the fixed set of
// format 0 context keys and the row chain position.
func historicalRowHash(t *testing.T, orgID uuid.UUID, row audit.Row) []byte {
	t.Helper()
	historicalContext := map[string]any{
		"org_id": orgID, "workspace_id": uuid.Nil, "scope_id": uuid.Nil, "parent_id": uuid.Nil,
		"source": audit.SourceMCP, "tool": "tack_get_issue",
	}
	payload := map[string]any{
		"org_id": orgID, "shard": row.Shard, "seq": row.Seq, "event_id": row.EventID,
		"event_time": row.EventTime.UTC().Format(time.RFC3339Nano),
		"actor_id":   row.ActorID, "actor_kind": audit.ActorUser, "action": row.Action,
		"entity_kind": row.EntityKind, "entity_id": row.EntityID, "pii_ref": uuid.Nil,
		"context": historicalContext, "delta": nil, "idempotency": "",
		"outcome": audit.OutcomeOK, "error": nil, "extra": nil, "hash_version": historicalHashVersion,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the historical hash payload: %v", err)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	hash := sha256.New()
	hash.Write(row.PrevHash)
	hash.Write(length[:])
	hash.Write(canonical)
	return hash.Sum(nil)
}
