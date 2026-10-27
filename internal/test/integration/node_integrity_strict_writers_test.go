package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictEveryWriterAdvancesTheFence runs each public product
// write operation through the service with real FoundationDB: create, update,
// parent move, relationship add, relationship remove, delete, delete with a
// moved child, and subtree delete. The test checks that each operation
// increases the write fence bucket epoch for every node that the operation
// writes. The delete of the epic moves the issue under the project. The delete
// of the project also deletes its descendants, including the issue.
func TestNodeIntegrityStrictEveryWriterAdvancesTheFence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actorID := uuid.New()
	workspace := createSchemaNode(t, env, env.OrgID, env.OrgID, "workspace", schemaProps("slug", quoted("main")))
	project := createSchemaNode(t, env, workspace.ID, workspace.ID, "project", map[string]json.RawMessage{
		"slug": quoted("fence"), "identifier": quoted("FENCE"),
	})
	label := createSchemaNode(t, env, workspace.ID, workspace.ID, "label", nil)
	epic := createSchemaNode(t, env, project.ID, project.ID, "epic", nil)

	var issue *node.NodeView
	requireFenceAdvances(t, env, "create", func() []uuid.UUID {
		issue = createSchemaNode(t, env, project.ID, project.ID, "issue", nil)
		return []uuid.UUID{issue.ID}
	})
	requireFenceAdvances(t, env, "update", func() []uuid.UUID {
		updateStrictPriority(t, env, issue.ID, "high")
		return []uuid.UUID{issue.ID}
	})
	requireFenceAdvances(t, env, "parent move", func() []uuid.UUID {
		update := service.UpdateInput{
			NodeID: issue.ID, Name: nil, Props: schemaProps("parent_id", quoted(epic.ID.String())),
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: actorID,
		}
		if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
			t.Fatalf("move issue %s under epic %s: %v", issue.ID, epic.ID, err)
		}
		return []uuid.UUID{issue.ID}
	})
	requireFenceAdvances(t, env, "relationship add", func() []uuid.UUID {
		relationship := &node.Relationship{OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelLabeledWith, TargetID: label.ID, CreatedBy: actorID}
		if err := env.NodeSvc.AddRelationship(env.Ctx, relationship); err != nil {
			t.Fatalf("label issue %s: %v", issue.ID, err)
		}
		return []uuid.UUID{issue.ID}
	})
	requireFenceAdvances(t, env, "relationship remove", func() []uuid.UUID {
		if err := env.NodeSvc.RemoveRelationship(env.Ctx, env.OrgID, issue.ID, node.RelLabeledWith, label.ID); err != nil {
			t.Fatalf("remove the label of issue %s: %v", issue.ID, err)
		}
		return []uuid.UUID{issue.ID}
	})
	requireFenceAdvances(t, env, "delete", func() []uuid.UUID {
		deleteStrictNode(t, env, label.ID, actorID)
		return []uuid.UUID{label.ID}
	})
	requireFenceAdvances(t, env, "delete with a moved child", func() []uuid.UUID {
		deleteStrictNode(t, env, epic.ID, actorID)
		return []uuid.UUID{epic.ID, issue.ID}
	})
	states, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", quoted(project.ID.String()))
	if err != nil {
		t.Fatalf("list the states of project %s: %v", project.ID, err)
	}
	requireFenceAdvances(t, env, "subtree delete", func() []uuid.UUID {
		deleteStrictNode(t, env, project.ID, actorID)
		deleted := []uuid.UUID{project.ID, issue.ID}
		for _, state := range states {
			deleted = append(deleted, state.ID)
		}
		return deleted
	})
}

// requireFenceAdvances calls CaptureStrictEvidence before and after it calls
// the write callback. The callback runs one operation and returns the IDs of
// the nodes that the operation writes. For each of the 64 buckets, the helper
// requires the epoch advance to be at least the number of returned node IDs
// in that bucket. A bucket without a returned node ID must keep the same epoch.
func requireFenceAdvances(t *testing.T, env *integration.TestEnv, operation string, write func() []uuid.UUID) {
	t.Helper()
	before, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence before the %s: %v", operation, err)
	}
	written := write()
	after, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence after the %s: %v", operation, err)
	}
	expected := make(map[int]int64, len(written))
	for _, nodeID := range written {
		expected[node.WriteFenceBucket(env.OrgID, nodeID)]++
	}
	for bucket := range node.WriteFenceBuckets {
		advance := after.Buckets[bucket] - before.Buckets[bucket]
		if advance < expected[bucket] || (expected[bucket] == 0 && advance != 0) {
			t.Fatalf("the %s advanced bucket %d by %d, want at least %d and no advance without a written node %v",
				operation, bucket, advance, expected[bucket], written)
		}
	}
}

// deleteStrictNode deletes nodeID through NodeService.Delete and calls
// ResumeSubtreeDeletes with a zero staleness bound to finish any subtree delete
// job that the request budget leaves running.
func deleteStrictNode(t *testing.T, env *integration.TestEnv, nodeID, actorID uuid.UUID) {
	t.Helper()
	if _, err := env.NodeSvc.Delete(env.Ctx, nodeID, actorID); err != nil {
		t.Fatalf("delete node %s: %v", nodeID, err)
	}
	if _, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0, env.LedgerResumeAudit()); err != nil {
		t.Fatalf("finish the delete of node %s: %v", nodeID, err)
	}
}
