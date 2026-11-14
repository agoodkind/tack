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

// TestServiceUpdateRefusesMoveOntoHeldReference checks the service update path
// when the destination scope already owns the rendered reference. The update
// must reject the conflict and preserve the node's scope and existing ownership.
func TestServiceUpdateRefusesMoveOntoHeldReference(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	setScopedSequenceTemplate(t, env, "issue")

	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	first := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	second := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	resident := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    first.ID,
			ScopeID:     first.ID,
			NodeTypeKey: "issue",
			Name:        "Resident",
			ActorID:     actor,
		},
	)
	mover := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    second.ID,
			ScopeID:     second.ID,
			NodeTypeKey: "issue",
			Name:        "Mover",
			ActorID:     actor,
		},
	)

	_, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID: mover.ID,
			Props: map[string]json.RawMessage{
				"parent_id": jsonStr(first.ID.String()),
			},
			ActorID: actor,
			ScopeID: first.ID,
		},
	)
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

// TestServiceUpdateReclaimsReferenceOnCleanMove checks that an update releases
// the old scope's reference and claims the unoccupied destination reference.
func TestServiceUpdateReclaimsReferenceOnCleanMove(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	setScopedSequenceTemplate(t, env, "issue")

	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	mover := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    origin.ID,
			ScopeID:     origin.ID,
			NodeTypeKey: "issue",
			Name:        "Mover",
			ActorID:     actor,
		},
	)
	if _, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID: mover.ID,
			Props: map[string]json.RawMessage{
				"parent_id": jsonStr(destination.ID.String()),
			},
			ActorID: actor,
			ScopeID: destination.ID,
		},
	); err != nil {
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

// setScopedSequenceTemplate declares a reference template for the specified
// types. It joins the scope reference, a hyphen, and a generated sequence.
// Tests supply templates explicitly because templates are org data.
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

		nodeType.ReferenceTemplates = []node.ReferenceTemplate{
			{
				Name:      "reference",
				IsPrimary: true,
				Generated: "sequence",
				Parts: []node.ReferencePart{
					{Kind: node.ReferencePartScopeRef, Value: node.FeatureIsScope},
					{Kind: node.ReferencePartLiteral, Value: "-"},
					{Kind: node.ReferencePartProperty, Value: "sequence"},
				},
			},
		}
		if err := env.Stores.NodeTypes.Set(env.Ctx, nodeType); err != nil {
			t.Fatalf("set template for %s: %v", nodeType.TypeKey, err)
		}
	}
}
