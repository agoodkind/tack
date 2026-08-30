package audit

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestLegacyEventCarriesTheDeleteShape pins the row shape production actually
// holds for the deletion that blocks the reference-key reconstruction: the
// node.delete verb, the tool that performed it, and the mcp_tool entity kind
// the pre-fix build stamped, which is why the row names no node. A testbed
// cannot reproduce the production case without all three.
func TestLegacyEventCarriesTheDeleteShape(t *testing.T) {
	t.Parallel()
	in := LegacyRowInput{
		OrgID: uuid.New(), ActorID: uuid.New(), EntityID: uuid.Nil, EventID: uuid.Nil,
		Action: string(VerbNodeDelete), Tool: "tack_delete_issue",
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
		t.Fatalf("entity id = %s, want the zero id production's rows carry", event.Entity.ID)
	}
}

// TestLegacyEventKeepsTheToolInvocationDefault pins that the original TACK-435
// shape is unchanged when the new fields are absent, so the pre-column outcome
// proof still writes the row it always wrote.
func TestLegacyEventKeepsTheToolInvocationDefault(t *testing.T) {
	t.Parallel()
	entityID := uuid.New()
	in := LegacyRowInput{
		OrgID: uuid.New(), ActorID: uuid.New(), EntityID: entityID, EventID: uuid.Nil,
		Action: "", Tool: "",
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
