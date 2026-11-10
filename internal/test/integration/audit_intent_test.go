package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestStateChangeCommitsItsLedgerRowInTheSameTransaction creates a node under
// an MCP tool slot. The create transaction must write the node.create change
// event with the caller and the tool, and the slot must report the commit. A
// refused transaction must not write a change event or report a commit.
func TestStateChangeCommitsItsLedgerRowInTheSameTransaction(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.New()
	ctx := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_create_workspace", actor)
	mark := outboxTail(t, env)

	created, err := env.NodeSvc.Create(ctx, service.CreateInput{
		ParentID: env.OrgID, ScopeID: env.OrgID, NodeTypeKey: "workspace", Name: "Main", Props: nil,
		Relationships: nil, ActorID: actor, IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !auditintent.Committed(ctx) {
		t.Fatal("the store must report the change events committed with the node")
	}
	operationID, err := auditintent.OperationID(ctx)
	if err != nil {
		t.Fatalf("read the slot operation: %v", err)
	}
	create := requireOneEvent(t, outboxEventsAfter(t, env, mark), audit.VerbNodeCreate, created.View.ID)
	requireChangeEvent(t, create, operationID, actor)
	if create.Context.Tool != "tack_create_workspace" {
		t.Fatalf("event tool = %q, want tack_create_workspace", create.Context.Tool)
	}

	// The first node claims the reference. The store refuses the second
	// claim and rolls back the whole write.
	key := []node.ReferenceKey{{TemplateName: "reference", Encoded: "TACK-173"}}
	first := newIntentTestNode(env.OrgID, created.View.ID)
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, nil, nil, nil, key, nil); err != nil {
		t.Fatalf("claim the reference: %v", err)
	}
	refusedMark := outboxTail(t, env)
	refused := auditintent.WithSlot(audit.WithScopeBuilder(env.Ctx), "tack_create_issue", actor)
	second := newIntentTestNode(env.OrgID, created.View.ID)
	if err := env.Stores.Nodes.CreateAtomic(refused, second, nil, nil, nil, key, nil); err == nil {
		t.Fatal("the second claim on one reference must be refused")
	}
	if auditintent.Committed(refused) {
		t.Fatal("a refused transaction must not report a commit")
	}
	if after := outboxEventsAfter(t, env, refusedMark); len(after) != 0 {
		t.Fatalf("a refused write wrote outbox events %+v", after)
	}
}

func newIntentTestNode(orgID, parentID uuid.UUID) *node.Node {
	id := uuid.Must(uuid.NewV7())
	return &node.Node{
		ID: id, OrgID: orgID, NodeType: "issue", Name: "Intent " + id.String(),
		Props:     map[string]json.RawMessage{"parent_id": json.RawMessage(`"` + parentID.String() + `"`)},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
	}
}
