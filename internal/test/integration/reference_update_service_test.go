package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestServiceUpdateRefusesMoveOntoHeldReference drives the user-facing update
// path, not the storage layer directly. Moving a node into a project whose
// numbering already issued that node's number renders a reference another node
// holds, and the update must be refused. An update that ignored reference
// ownership would leave the moved node claiming its old value and nothing
// claiming the new one, which is the silent collision this work closes.
func TestServiceUpdateRefusesMoveOntoHeldReference(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	setScopedSequenceTemplate(t, env, "issue")

	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	first := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	second := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	resident := mustCreate(t, env, service.CreateInput{
		ParentID: first.ID, ScopeID: first.ID, NodeTypeKey: "issue", Name: "Resident", ActorID: actor,
	})
	mover := mustCreate(t, env, service.CreateInput{
		ParentID: second.ID, ScopeID: second.ID, NodeTypeKey: "issue", Name: "Mover", ActorID: actor,
	})

	_, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: mover.ID,
		Props: map[string]json.RawMessage{
			"scope_id":  jsonStr(first.ID.String()),
			"parent_id": jsonStr(first.ID.String()),
		},
		ActorID: actor,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("update moving %s into %s error = %v, want ErrConflict", mover.ID, first.ID, err)
	}

	stored, err := env.Stores.Views.Get(env.Ctx, mover.ID)
	if err != nil {
		t.Fatalf("get moved node: %v", err)
	}
	if got := stringPropOf(stored, "scope_id"); got != second.ID.String() {
		t.Fatalf("scope_id after refused move = %q, want %q", got, second.ID)
	}
	owner, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-1")
	if err != nil {
		t.Fatalf("lookup contested reference: %v", err)
	}
	if owner != resident.ID {
		t.Fatalf("FAN-1 owner = %s, want the resident %s", owner, resident.ID)
	}
}

// TestServiceUpdateReclaimsReferenceOnCleanMove proves the same path succeeds
// when nothing contests the rendered value, and that the node ends up holding
// the reference its new scope produces rather than its old one.
func TestServiceUpdateReclaimsReferenceOnCleanMove(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	setScopedSequenceTemplate(t, env, "issue")

	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	mover := mustCreate(t, env, service.CreateInput{
		ParentID: origin.ID, ScopeID: origin.ID, NodeTypeKey: "issue", Name: "Mover", ActorID: actor,
	})
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: mover.ID,
		Props: map[string]json.RawMessage{
			"scope_id":  jsonStr(destination.ID.String()),
			"parent_id": jsonStr(destination.ID.String()),
		},
		ActorID: actor,
	}); err != nil {
		t.Fatalf("clean move: %v", err)
	}

	released, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-1")
	if err != nil {
		t.Fatalf("lookup released reference: %v", err)
	}
	if released != uuid.Nil {
		t.Fatalf("FAN-1 owner after move = %s, want it released", released)
	}
	held, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "QAT-1")
	if err != nil {
		t.Fatalf("lookup new reference: %v", err)
	}
	if held != mover.ID {
		t.Fatalf("QAT-1 owner = %s, want the moved node %s", held, mover.ID)
	}
}

// setScopedSequenceTemplate declares, for the named types, the reference shape
// this deployment uses: the enclosing scope's reference, a hyphen, then a
// generated sequence. Templates are org data, so a test that needs one declares
// it rather than relying on any shape built into the engine.
func setScopedSequenceTemplate(t *testing.T, env *TestEnv, typeKeys ...string) {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	wanted := make(map[string]bool, len(typeKeys))
	for _, typeKey := range typeKeys {
		wanted[typeKey] = true
	}
	for _, nodeType := range types {
		if !wanted[nodeType.TypeKey] {
			continue
		}
		nodeType.ReferenceTemplates = []node.ReferenceTemplate{{
			Name:      "reference",
			IsPrimary: true,
			Generated: "sequence",
			Parts: []node.ReferencePart{
				{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
				{Kind: node.ReferencePartLiteral, Value: "-"},
				{Kind: node.ReferencePartProperty, Value: "sequence"},
			},
		}}
		if err := env.Stores.NodeTypes.Set(env.Ctx, nodeType); err != nil {
			t.Fatalf("set template for %s: %v", nodeType.TypeKey, err)
		}
	}
}
