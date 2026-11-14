package audit

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestLegacyEventUsesHistoricalDeleteFields checks the historical deletion record
// required to reproduce the blocked reference-key reconstruction. It requires
// node.delete, the deleting tool, and mcp_tool without a node ID.
func TestLegacyEventUsesHistoricalDeleteFields(t *testing.T) {
	t.Parallel()
	in := LegacyRowInput{
		OrgID:    uuid.New(),
		ActorID:  uuid.New(),
		EntityID: uuid.Nil,
		EventID:  uuid.Nil,
		Action:   string(VerbNodeDelete),
		Tool:     "tack_delete_issue",
	}
	row := LegacyRow{EventID: uuid.New(), Shard: 3, Seq: 7, EventTime: time.Now().UTC()}

	event := legacyEvent(in, row)

	if event.Verb != string(VerbNodeDelete) {
		t.Fatalf("verb = %q, want %q", event.Verb, VerbNodeDelete)
	}

	if event.Context.Tool != "tack_delete_issue" {
		t.Fatalf("tool = %q, want tack_delete_issue", event.Context.Tool)
	}

	if event.Entity.Type != legacyEntityKind {
		t.Fatalf("entity kind = %q, want %q", event.Entity.Type, legacyEntityKind)
	}

	if event.Entity.ID != uuid.Nil {
		t.Fatalf("entity id = %s, want the zero id in historical production rows", event.Entity.ID)
	}
}

// TestLegacyEventKeepsTheToolInvocationDefault checks the original TACK-435
// event shape when Action and Tool are absent.
func TestLegacyEventKeepsTheToolInvocationDefault(t *testing.T) {
	t.Parallel()
	entityID := uuid.New()
	in := LegacyRowInput{
		OrgID:    uuid.New(),
		ActorID:  uuid.New(),
		EntityID: entityID,
		EventID:  uuid.Nil,
		Action:   "",
		Tool:     "",
	}
	row := LegacyRow{EventID: uuid.New(), Shard: 1, Seq: 1, EventTime: time.Now().UTC()}

	event := legacyEvent(in, row)

	if event.Verb != string(VerbMCPToolInvoked) {
		t.Fatalf("verb = %q, want %q", event.Verb, VerbMCPToolInvoked)
	}

	if event.Entity.Type != "node" || event.Entity.ID != entityID {
		t.Fatalf("entity = %+v, want the node %s", event.Entity, entityID)
	}
}
