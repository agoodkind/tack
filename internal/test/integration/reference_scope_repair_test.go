package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

// TestScopeRepairReportsRefusedWrites checks that RepairSequenceScopeIDs returns
// an error that wraps domain.ErrConflict and zero repaired nodes when another
// issue owns QAT-1. The test checks that the stale issue keeps scope_id Fan and
// the holder issue keeps the claim on QAT-1.
func TestScopeRepairReportsRefusedWrites(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)
	holder := writeNodeWithSplitScope(t, env, "issue", "Holder", destination.ID, destination.ID, 1)
	held := node.ReferenceKey{TemplateName: "reference", Encoded: "QAT-1"}
	if err := env.Stores.Nodes.SetReferenceKeys(
		env.Ctx,
		env.OrgID,
		holder.ID,
		[]node.ReferenceKey{held},
	); err != nil {
		t.Fatalf("SetReferenceKeys failed for the holder issue: %v", err)
	}

	stale := writeNodeWithSplitScope(t, env, "issue", "Stale", destination.ID, origin.ID, 1)

	repaired, _, err := ops.RepairSequenceScopeIDs(env.Ctx, env.Ops)
	if !errors.Is(err, domain.ErrConflict) || repaired != 0 {
		t.Fatalf(
			"RepairSequenceScopeIDs returned %d repaired nodes and error %v; TestScopeRepairReportsRefusedWrites expects an error that wraps domain.ErrConflict and zero repaired nodes",
			repaired,
			err,
		)
	}

	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, stale.ID)
	if err != nil || stored == nil ||
		string(stored.Props["scope_id"]) != string(jsonStr(origin.ID.String())) {
		t.Fatalf(
			"The read of stale issue %s returned node %v and error %v; the stale issue must keep scope_id %s",
			stale.ID,
			stored,
			err,
			origin.ID,
		)
	}

	owner, err := env.Stores.Nodes.LookupReference(
		env.Ctx,
		env.OrgID,
		held.TemplateName,
		held.Encoded,
	)
	if err != nil || owner != holder.ID {
		t.Fatalf(
			"The QAT-1 lookup returned owner %s and error %v; holder issue %s must retain the claim",
			owner,
			err,
			holder.ID,
		)
	}
}

// TestScopeRepairMovesTheReferenceWithTheNode checks that changing the owning
// scope releases the old reference and assigns the new reference to the node.
func TestScopeRepairMovesTheReferenceWithTheNode(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	// The parent identifies the second project, but scope_id still identifies
	// the first project. The repair must correct this stale scope pointer.
	stale := writeNodeWithSplitScope(t, env, "issue", "Stale", destination.ID, origin.ID, 1)
	if err := env.Stores.Nodes.SetReferenceKeys(
		env.Ctx,
		env.OrgID,
		stale.ID,
		[]node.ReferenceKey{{TemplateName: "reference", Encoded: "FAN-1"}},
	); err != nil {
		t.Fatalf("seed the node's existing claim: %v", err)
	}

	repaired, skipped, err := ops.RepairSequenceScopeIDs(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("RepairSequenceScopeIDs: %v", err)
	}

	if repaired == 0 {
		t.Fatalf("repaired = %d, skipped = %d, want the stale node repaired", repaired, skipped)
	}

	released, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-1")
	if err != nil {
		t.Fatalf("lookup old reference: %v", err)
	}

	if released != uuid.Nil {
		t.Fatalf("FAN-1 owner after the move = %s, want it released", released)
	}

	held, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "QAT-1")
	if err != nil {
		t.Fatalf("lookup new reference: %v", err)
	}

	if held != stale.ID {
		t.Fatalf("QAT-1 owner = %s, want the moved node %s", held, stale.ID)
	}
}

// TestScopeRepairPreservesClaimsWithoutTypeTemplates checks that a scope repair
// preserves existing reference claims when the type does not declare templates.
// An empty reference list would explicitly release those claims.
func TestScopeRepairPreservesClaimsWithoutTypeTemplates(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	clearReferenceTemplates(t, env, "issue")
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	stale := writeNodeWithSplitScope(t, env, "issue", "Stale", destination.ID, origin.ID, 1)
	held := node.ReferenceKey{TemplateName: "reference", Encoded: "FAN-1"}
	if err := env.Stores.Nodes.SetReferenceKeys(
		env.Ctx,
		env.OrgID,
		stale.ID,
		[]node.ReferenceKey{held},
	); err != nil {
		t.Fatalf("seed the node's existing claim: %v", err)
	}

	if _, _, err := ops.RepairSequenceScopeIDs(env.Ctx, env.Ops); err != nil {
		t.Fatalf("RepairSequenceScopeIDs: %v", err)
	}

	owner, err := env.Stores.Nodes.LookupReference(
		env.Ctx,
		env.OrgID,
		held.TemplateName,
		held.Encoded,
	)
	if err != nil {
		t.Fatalf("lookup the claim: %v", err)
	}

	if owner != stale.ID {
		t.Fatalf("FAN-1 owner after the repair = %s, want the node keeping it, %s", owner, stale.ID)
	}
}

// clearReferenceTemplates removes templates from the specified types to test
// org-defined types without reference constraints.
func clearReferenceTemplates(t *testing.T, env *TestEnv, typeKeys ...string) {
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

		nodeType.ReferenceTemplates = nil
		if err := env.Stores.NodeTypes.Set(env.Ctx, nodeType); err != nil {
			t.Fatalf("clear templates for %s: %v", nodeType.TypeKey, err)
		}
	}
}

// writeNodeWithSplitScope writes different node IDs for parent_id and scope_id
// to create the inconsistency that scope repair corrects.
func writeNodeWithSplitScope(
	t *testing.T,
	env *TestEnv,
	typeKey string,
	name string,
	parentID uuid.UUID,
	scopeID uuid.UUID,
	sequence int,
) *node.NodeView {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": jsonStr(parentID.String()),
		"scope_id":  jsonStr(scopeID.String()),
		"sequence":  jsonNumber(sequence),
	}
	current := &node.Node{
		ID:        uuid.Must(uuid.NewV7()),
		OrgID:     env.OrgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        current.ID,
		OrgID:     current.OrgID,
		NodeType:  current.NodeType,
		Name:      current.Name,
		Props:     props,
		CreatedAt: current.CreatedAt,
		UpdatedAt: current.UpdatedAt,
	}
	if err := env.Stores.Nodes.CreateAtomic(
		env.Ctx,
		current,
		view,
		nil,
		[]string{"parent_id", "scope_id", "sequence"},
		nil,
		nil,
	); err != nil {
		t.Fatalf("write %s with split scope: %v", typeKey, err)
	}

	return view
}
