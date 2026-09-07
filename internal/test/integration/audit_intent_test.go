package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestStateChangeCommitsItsLedgerRowInTheSameTransaction is TACK-173 against
// the real store: a create made under the tool wrapper's slot leaves its
// ledger row in the FoundationDB outbox, committed by the same transaction
// that wrote the node, and the slot reports the write so the wrapper records
// nothing further. The negative case follows: a transaction the store refuses
// leaves no row behind, because there is no change for it to describe.
func TestStateChangeCommitsItsLedgerRowInTheSameTransaction(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ctx := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_create_workspace", actor)

	created, err := env.NodeSvc.Create(ctx, service.CreateInput{
		ParentID: env.OrgID, ScopeID: env.OrgID, NodeTypeKey: "workspace", Name: "Main", ActorID: actor,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !auditintent.Committed(ctx) {
		t.Fatal("the store must report the staged row committed with the node")
	}
	entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, nil, 10)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("outbox entries = %d, want the one row the create committed", len(entries))
	}
	var event audit.Event
	if err := json.Unmarshal(entries[0].Event, &event); err != nil {
		t.Fatalf("decode the outbox row: %v", err)
	}
	if event.Verb != string(audit.VerbNodeCreate) || event.Entity.ID != created.View.ID {
		t.Fatalf("outbox row = %+v, want node.create of %s", event, created.View.ID)
	}
	if event.Actor.ID != actor || event.Context.Tool != "tack_create_workspace" {
		t.Fatalf("outbox row = %+v, want the caller and the tool", event)
	}

	// A refused transaction: the second node claims a reference the first
	// already holds, so the store rolls the whole write back.
	key := []node.ReferenceKey{{TemplateName: "reference", Encoded: "TACK-173"}}
	first := newIntentTestNode(env.OrgID, created.View.ID)
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, nil, nil, nil, key, nil); err != nil {
		t.Fatalf("claim the reference: %v", err)
	}
	refused := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_create_issue", actor)
	if err := audit.StageStateChange(refused, audit.VerbNodeCreate, audit.Entity{Type: "node", ID: uuid.New()}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	second := newIntentTestNode(env.OrgID, created.View.ID)
	err = env.Stores.Nodes.CreateAtomic(refused, second, nil, nil, nil, key, nil)
	if err == nil {
		t.Fatal("the second claim on one reference must be refused")
	}
	if auditintent.Committed(refused) {
		t.Fatal("a refused transaction must not report its row committed")
	}
	entries, err = env.Stores.OpsOutbox.ReadOutboxFrom(env.Ctx, nil, 10)
	if err != nil {
		t.Fatalf("read the outbox after the refusal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("outbox entries = %d after a refused write, want still 1: no change, no row", len(entries))
	}
}

func newIntentTestNode(orgID, parentID uuid.UUID) *node.Node {
	id := uuid.Must(uuid.NewV7())
	return &node.Node{
		ID: id, OrgID: orgID, NodeType: "issue", Name: "Intent " + id.String(),
		Props:     map[string]json.RawMessage{"parent_id": mustJSON(parentID.String())},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
	}
}
