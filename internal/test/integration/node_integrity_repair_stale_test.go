package integration_test

import (
	"maps"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestRepairWriteRefusesNodeMovedAfterRead creates a project, an epic, and an
// issue through NodeService. The test reads the issue with GetForUpdate, moves
// the issue under the epic with NodeService.Update, and attempts a scope_id
// repair with UpdateAtomicFrom and the old digest. The test requires
// domain.ErrConflict, parent_id at the epic, and exactly one child_of edge to
// the epic. A second read and write with the current digest must succeed.
func TestRepairWriteRefusesNodeMovedAfterRead(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	epic := createSchemaNode(t, env, projectID, projectID, "epic", nil)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	read, readDigest := readForRepair(t, env, issue.ID)
	move := service.UpdateInput{
		NodeID: issue.ID, Name: nil, Props: schemaProps("parent_id", quoted(epic.ID.String())),
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}
	if _, err := env.NodeSvc.Update(env.Ctx, move); err != nil {
		t.Fatalf("NodeService.Update could not move issue %s under epic %s: %v", issue.ID, epic.ID, err)
	}

	err := writeRepairedScope(env, read, readDigest, projectID)
	requireError(t, err, domain.ErrConflict, "repair write after the parent move")
	if got := string(storedStoreView(t, env.Stores, issue.ID).Props["parent_id"]); got != string(quoted(epic.ID.String())) {
		t.Fatalf("issue %s has parent_id %s after the refused repair write; the test expects epic %s", issue.ID, got, epic.ID)
	}
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != epic.ID {
		t.Fatalf("issue %s child_of lookup returned %v, %v after the refused repair write; the test expects one edge to epic %s",
			issue.ID, edges, err, epic.ID)
	}

	current, currentDigest := readForRepair(t, env, issue.ID)
	if err := writeRepairedScope(env, current, currentDigest, projectID); err != nil {
		t.Fatalf("repair write of issue %s from the current record failed: %v", issue.ID, err)
	}
}

// readForRepair reads nodeID with GetForUpdate as the ops repairs do. The
// function fails the test if the read fails or the node does not exist.
func readForRepair(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) (*node.Node, node.Digest) {
	t.Helper()
	read, digest, err := env.Stores.Nodes.GetForUpdate(env.Ctx, env.OrgID, nodeID)
	if err != nil || read == nil {
		t.Fatalf("repair read of node %s failed: %v, %v", nodeID, read, err)
	}
	return read, digest
}

// writeRepairedScope copies read, sets scope_id to scopeID, and writes the copy
// with UpdateAtomicFrom and readDigest as repair.sequence_scope_ids does.
// The function returns the error from UpdateAtomicFrom.
func writeRepairedScope(env *integration.TestEnv, read *node.Node, readDigest node.Digest, scopeID uuid.UUID) error {
	oldProps := maps.Clone(read.Props)
	repaired := *read
	repaired.Props = maps.Clone(read.Props)
	repaired.Props["scope_id"] = quoted(scopeID.String())
	view := &node.NodeView{
		ID: repaired.ID, OrgID: repaired.OrgID, NodeType: repaired.NodeType, Name: repaired.Name, Props: maps.Clone(repaired.Props),
		CreatedBy: repaired.CreatedBy, UpdatedBy: repaired.UpdatedBy, CreatedAt: repaired.CreatedAt, UpdatedAt: repaired.UpdatedAt,
	}
	return env.Stores.Nodes.UpdateAtomicFrom(env.Ctx, readDigest, &repaired, view, oldProps, nil, nil)
}
