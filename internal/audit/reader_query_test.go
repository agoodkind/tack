package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildAuditQueryFiltersContextCorrelation(t *testing.T) {
	filter := QueryFilter{
		OrgID:     uuid.Must(uuid.NewV7()),
		Oldest:    time.Unix(10, 0).UTC(),
		Latest:    time.Unix(20, 0).UTC(),
		Action:    "node.read",
		ActorID:   uuid.Must(uuid.NewV7()),
		EntityID:  uuid.Must(uuid.NewV7()),
		RequestID: "req-123",
		TraceID:   "trace-456",
		Limit:     17,
	}

	query, args := buildAuditQuery(filter)

	for _, want := range []string{
		"action = $4",
		"actor_id = $5",
		"entity_id = $6",
		"context->>'request_id' = $7",
		"context->>'trace_id' = $8",
		"LIMIT $9",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if len(args) != 9 {
		t.Fatalf("args len = %d, want 9", len(args))
	}
	if args[6] != filter.RequestID {
		t.Fatalf("request arg = %v, want %s", args[6], filter.RequestID)
	}
	if args[7] != filter.TraceID {
		t.Fatalf("trace arg = %v, want %s", args[7], filter.TraceID)
	}
}

// TestBuildAuditQueryWithoutALimitAsksForEveryRow pins the statement the
// streaming export runs. A zero limit must not fall back to a page, or the
// bundle stops short of the ledger; and it must not carry an ORDER BY, because
// no index serves event_time order under an org-only filter, so ordering an
// unbounded result makes the database hold every matching row before it returns
// the first one.
func TestBuildAuditQueryWithoutALimitAsksForEveryRow(t *testing.T) {
	filter := QueryFilter{
		OrgID:     uuid.Must(uuid.NewV7()),
		Oldest:    time.Unix(10, 0).UTC(),
		Latest:    time.Unix(20, 0).UTC(),
		Action:    "",
		ActorID:   uuid.Nil,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     0,
	}

	query, args := buildAuditQuery(filter)

	if strings.Contains(query, "LIMIT") {
		t.Fatalf("a whole-range export carries a LIMIT:\n%s", query)
	}
	if strings.Contains(query, "ORDER BY") {
		t.Fatalf("a whole-range export carries an ORDER BY:\n%s", query)
	}
	if len(args) != 3 {
		t.Fatalf("args len = %d, want the org and the two time bounds", len(args))
	}
}

func TestCanonicalizeCorrelationKeepsRequestIDInContext(t *testing.T) {
	ev := Event{
		Actor:   Actor{RequestID: "req-from-actor"},
		Context: EventContext{},
	}

	canonicalizeCorrelation(&ev)

	if ev.Context.RequestID != "req-from-actor" {
		t.Fatalf("context request_id = %q", ev.Context.RequestID)
	}
	if hasPII(ev.Actor) {
		t.Fatalf("actor request_id should not create audit.pii payload")
	}
}
