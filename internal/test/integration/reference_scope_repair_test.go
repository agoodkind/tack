package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
)

// TestScopeRepairMovesTheReferenceWithTheNode drives the scope repair, which
// rewrites a node's owning scope. The reference the node renders changes with
// that scope, so the repair must claim the new value and release the old one. A
// repair that moved the node without touching its claim would leave the old
// value owned by a node no longer in that scope and the new value owned by
// nobody, which is the collision this work exists to prevent.
func TestScopeRepairMovesTheReferenceWithTheNode(t *testing.T) {
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	origin := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	destination := mustCreateScope(t, env, "project", "Qat", workspace.ID, workspace.ID, actor)

	// The node sits under the second project but still names the first as its
	// scope, which is the stale pointer this repair corrects.
	stale := writeNodeWithSplitScope(t, env, "issue", "Stale", destination.ID, origin.ID, 1)
	if err := env.Stores.Nodes.SetReferenceKeys(env.Ctx, env.OrgID, stale.ID,
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

// writeNodeWithSplitScope writes a node whose parent and scope pointers name
// different nodes, the shape the scope repair exists to correct.
func writeNodeWithSplitScope(
	t *testing.T,
	env *TestEnv,
	typeKey, name string,
	parentID, scopeID uuid.UUID,
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
		env.Ctx, current, view, nil,
		[]string{"parent_id", "scope_id", "sequence"}, nil, nil,
	); err != nil {
		t.Fatalf("write %s with split scope: %v", typeKey, err)
	}
	return view
}
