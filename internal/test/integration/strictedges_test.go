package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictRejectsInvalidCreateAndEdgeAfterActivation checks
// that strict validation rejects an issue creation under a label through
// NodeService.Create and rejects RelationshipStore.Add calls for a child_of
// edge and an edge to the organization node of another organization. It
// checks that each error wraps domain.ErrInvalidArgument and that the project
// still lists only the existing issue. It also checks that the issue retains
// exactly one child_of edge to the project and does not have any labeled_with
// edges.
func TestNodeIntegrityStrictRejectsInvalidCreateAndEdgeAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	workspaceID := storedParentID(t, env, projectID)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	label := createSchemaNode(t, env, workspaceID, workspaceID, "label", nil)
	otherOrgID := createStrictOtherOrg(t, env)
	activateStrict(t)

	_, err := env.NodeSvc.Create(env.Ctx, schemaCreateInput(label.ID, projectID, "issue", nil))
	requireError(t, err, domain.ErrInvalidArgument, "the create of an issue under a label")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 1 {
		t.Fatalf(
			"project %s lists %d issues after the refused create, want 1",
			projectID,
			len(issues),
		)
	}

	for name, relationship := range map[string]*node.Relationship{
		"a child_of edge": {
			OrgID:        env.OrgID,
			SourceID:     issue.ID,
			RelationType: node.RelChildOf,
			TargetID:     workspaceID,
			CreatedBy:    uuid.New(),
		},
		"an edge to another organization": {
			OrgID:        env.OrgID,
			SourceID:     issue.ID,
			RelationType: node.RelLabeledWith,
			TargetID:     otherOrgID,
			CreatedBy:    uuid.New(),
		},
	} {
		requireError(
			t,
			env.Stores.Relationships.Add(env.Ctx, relationship),
			domain.ErrInvalidArgument,
			"the store add of "+name,
		)
	}

	parents, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issue.ID,
		node.RelChildOf,
	)
	if err != nil || len(parents) != 1 || parents[0].TargetID != projectID {
		t.Fatalf(
			"child_of edges of issue %s after the refused adds = %v, %v, want one edge to %s",
			issue.ID,
			parents,
			err,
			projectID,
		)
	}

	labels, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issue.ID,
		node.RelLabeledWith,
	)
	if err != nil || len(labels) != 0 {
		t.Fatalf(
			"labeled_with edges of issue %s after the refused add = %v, %v, want none",
			issue.ID,
			labels,
			err,
		)
	}
}

// TestNodeIntegrityStrictScanRefusesDisagreeingParentEdge adds a second
// child_of edge to an issue through RelationshipStore.Add before activation.
// With two parents, ops verify strict-validation must report a child_of
// violation for the issue and refuse activation.
func TestNodeIntegrityStrictScanRefusesDisagreeingParentEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	extra := &node.Relationship{
		OrgID:        env.OrgID,
		SourceID:     issue.ID,
		RelationType: node.RelChildOf,
		TargetID:     storedParentID(t, env, projectID),
		CreatedBy:    uuid.New(),
	}
	if err := env.Stores.Relationships.Add(env.Ctx, extra); err != nil {
		t.Fatalf("add a second child_of edge to issue %s: %v", issue.ID, err)
	}

	output, err := runStrictValidation(t, true)
	violations := output.Result.FirstViolations
	if err == nil || output.Result.Activated || len(violations) != 1 ||
		violations[0].NodeID != issue.ID ||
		!strings.Contains(violations[0].Reason, "child_of") {
		t.Fatalf(
			"strict validation = %+v, %v; want one child_of violation for issue %s",
			output.Result,
			err,
			issue.ID,
		)
	}

	stored, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || stored.Active {
		t.Fatalf("activation after the refused scan = %+v, %v; want inactive", stored, err)
	}
}

func activateStrict(t *testing.T) {
	t.Helper()
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated {
		t.Fatalf("strict validation = %+v, %v; want activation", output.Result, err)
	}
}

func storedParentID(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) uuid.UUID {
	t.Helper()
	snapshot, _ := mutationSnapshot(t, env, nodeID)
	if snapshot.ParentID == uuid.Nil {
		t.Fatalf("node %s does not have one hierarchy parent", nodeID)
	}

	return snapshot.ParentID
}
