package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/auditintent"
)

// TestStageStateChangeStagesTheRowTheWrapperWouldHaveRecorded pins the
// contract between the service and the store: the staged payload decodes to
// the event the ledger expects, with a fixed id and time so a relay retry
// delivers one row, the actor and tool the wrapper attached, and the scope
// the resolvers stamped.
func TestStageStateChangeStagesTheRowTheWrapperWouldHaveRecorded(t *testing.T) {
	actor := uuid.New()
	orgID := uuid.New()
	nodeID := uuid.New()
	ctx := auditintent.WithSlot(WithScopeBuilder(context.Background()), "tack_delete_issue", actor)
	SetScopeFields(ctx, Scope{OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil})

	err := StageStateChange(ctx, VerbNodeDelete, Entity{
		Type: "node", NodeType: "issue", ID: nodeID, Identifier: "TACK-1", Name: "Gone",
	})
	if err != nil {
		t.Fatalf("StageStateChange: %v", err)
	}
	payload, ok := auditintent.Pending(ctx)
	if !ok {
		t.Fatal("the event must be pending for the next transaction")
	}
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("the staged payload must decode as an event: %v", err)
	}
	if event.Verb != string(VerbNodeDelete) || event.Entity.ID != nodeID || event.Outcome != OutcomeOK {
		t.Fatalf("event = %+v, want node.delete of %s with outcome ok", event, nodeID)
	}
	if event.EventID == uuid.Nil || event.OccurredAt.IsZero() {
		t.Fatal("the event must carry its id and time before it is written, so a relay retry lands one row")
	}
	if event.Actor.ID != actor || event.Context.Tool != "tack_delete_issue" || event.Context.OrgID != orgID {
		t.Fatalf("event = %+v, want the wrapper's actor and tool and the stamped org", event)
	}
	if event.Context.Source != SourceMCP {
		t.Fatalf("source = %q, want mcp", event.Context.Source)
	}
}

// TestStageStateChangeIsInertOutsideTheWrapper pins that operator commands,
// seeds, and tests that never attached a slot stage nothing and keep the
// recording they already have.
func TestStageStateChangeIsInertOutsideTheWrapper(t *testing.T) {
	ctx := context.Background()

	if err := StageStateChange(ctx, VerbNodeCreate, Entity{Type: "node", ID: uuid.New()}); err != nil {
		t.Fatalf("StageStateChange: %v", err)
	}
	if _, ok := auditintent.Pending(ctx); ok {
		t.Fatal("nothing may be staged without a slot")
	}
}
