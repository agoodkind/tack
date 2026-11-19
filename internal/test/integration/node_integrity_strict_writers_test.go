package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
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
	workspace := createSchemaNode(
		t,
		env,
		env.OrgID,
		env.OrgID,
		"workspace",
		schemaProps("slug", quoted("main")),
	)
	project := createSchemaNode(
		t,
		env,
		workspace.ID,
		workspace.ID,
		"project",
		map[string]json.RawMessage{
			"slug":       quoted("fence"),
			"identifier": quoted("FENCE"),
		},
	)
	label := createSchemaNode(t, env, workspace.ID, workspace.ID, "label", nil)
	epic := createSchemaNode(t, env, project.ID, project.ID, "epic", nil)

	var issue *node.NodeView
	requireFenceAdvances(
		t,
		env,
		"create",
		func() []uuid.UUID {
			issue = createSchemaNode(t, env, project.ID, project.ID, "issue", nil)
			return []uuid.UUID{issue.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"update",
		func() []uuid.UUID {
			updateStrictPriority(t, env, issue.ID, "high")
			return []uuid.UUID{issue.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"parent move",
		func() []uuid.UUID {
			update := moveUpdate(issue.ID, epic.ID)
			update.ActorID = actorID
			if _, err := env.NodeSvc.Update(env.Ctx, update); err != nil {
				t.Fatalf("move issue %s under epic %s: %v", issue.ID, epic.ID, err)
			}

			return []uuid.UUID{issue.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"relationship add",
		func() []uuid.UUID {
			relationship := &node.Relationship{
				OrgID:        env.OrgID,
				SourceID:     issue.ID,
				RelationType: node.RelLabeledWith,
				TargetID:     label.ID,
				CreatedBy:    actorID,
			}
			if err := env.NodeSvc.AddRelationship(env.Ctx, relationship); err != nil {
				t.Fatalf("label issue %s: %v", issue.ID, err)
			}

			return []uuid.UUID{issue.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"relationship remove",
		func() []uuid.UUID {
			if err := env.NodeSvc.RemoveRelationship(
				env.Ctx,
				env.OrgID,
				issue.ID,
				node.RelLabeledWith,
				label.ID,
			); err != nil {
				t.Fatalf("remove the label of issue %s: %v", issue.ID, err)
			}

			return []uuid.UUID{issue.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"delete",
		func() []uuid.UUID {
			deleteStrictNode(t, env, label.ID, actorID)
			return []uuid.UUID{label.ID}
		},
	)
	requireFenceAdvances(
		t,
		env,
		"delete with a moved child",
		func() []uuid.UUID {
			deleteStrictNode(t, env, epic.ID, actorID)
			return []uuid.UUID{epic.ID, issue.ID}
		},
	)
	states, err := env.Stores.Views.List(
		env.Ctx,
		node.NodeListQuery{
			OrgID:            env.OrgID,
			NodeType:         "state",
			ByTargetRelation: &node.RelationScan{NodeID: project.ID, RelationType: node.RelChildOf},
		},
	)
	if err != nil {
		t.Fatalf("list the states of project %s: %v", project.ID, err)
	}

	requireFenceAdvances(
		t,
		env,
		"subtree delete",
		func() []uuid.UUID {
			deleteStrictNode(t, env, project.ID, actorID)
			deleted := []uuid.UUID{project.ID, issue.ID}

			for _, state := range states {
				deleted = append(deleted, state.ID)
			}

			return deleted
		},
	)
}
