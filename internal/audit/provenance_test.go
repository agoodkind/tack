package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/auditintent"
)

// A state change staged while an operator acts as a user keeps the user as
// its actor and carries the operator, the grant, and the reason in Extra,
// under the operator source (TACK-424).
func TestStageStateChangeCarriesActProvenance(t *testing.T) {
	user := uuid.New()
	operator := uuid.New()
	grant := uuid.New()
	ctx := auditintent.WithSlot(WithScopeBuilder(context.Background()), "ops_act_as_create", user)
	ctx = WithActProvenance(ctx, ActProvenance{
		OperatorID: operator, OperatorEmail: "ops@example.invalid", GrantID: grant, Reason: "fix a stuck board",
	})

	if err := StageStateChange(ctx, VerbNodeCreate, Entity{Type: "node", NodeType: "issue", ID: uuid.New(), Identifier: "", Name: "x"}); err != nil {
		t.Fatalf("StageStateChange: %v", err)
	}
	payload, ok := auditintent.Pending(ctx)
	if !ok {
		t.Fatal("the event must be pending for the next transaction")
	}
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode the staged event: %v", err)
	}
	if event.Actor.ID != user || event.Actor.Type != ActorUser {
		t.Fatalf("actor = %+v, want the user %s", event.Actor, user)
	}
	if event.Context.Source != SourceOperator {
		t.Fatalf("source = %q, want %q", event.Context.Source, SourceOperator)
	}
	var extra actAsExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode the extra: %v", err)
	}
	if extra.ActAs.OperatorID != operator || extra.ActAs.GrantID != grant || extra.ActAs.Reason != "fix a stuck board" {
		t.Fatalf("extra = %+v, want the operator, the grant, and the reason", extra.ActAs)
	}
}

// Without provenance the staged event is unchanged: no Extra, MCP source.
func TestStageStateChangeWithoutProvenanceCarriesNoExtra(t *testing.T) {
	ctx := auditintent.WithSlot(WithScopeBuilder(context.Background()), "tack_create_issue", uuid.New())
	if err := StageStateChange(ctx, VerbNodeCreate, Entity{Type: "node", NodeType: "issue", ID: uuid.New(), Identifier: "", Name: "x"}); err != nil {
		t.Fatalf("StageStateChange: %v", err)
	}
	payload, _ := auditintent.Pending(ctx)
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode the staged event: %v", err)
	}
	if len(event.Extra) != 0 || event.Context.Source != SourceMCP {
		t.Fatalf("event = %+v, want no extra and the mcp source", event)
	}
}
