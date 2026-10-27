package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/test/integration"
)

const strictMemberRole = 20

// TestNodeIntegrityStrictAcceptsMemberAssignmentAfterActivation creates a user
// who is a member of the organization, activates strict validation, and adds
// an assigned_to edge and a watches edge from an issue to the user's ID
// through NodeService.AddRelationship. Each add must succeed, and the issue
// must list both edges.
func TestNodeIntegrityStrictAcceptsMemberAssignmentAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	memberID := createStrictMember(t, env)
	activateStrict(t)

	for _, relationType := range []string{node.RelAssignedTo, node.RelWatches} {
		relationship := &node.Relationship{
			OrgID: env.OrgID, SourceID: issue.ID, RelationType: relationType, TargetID: memberID,
			CreatedBy: memberID, CreatedAt: clock.Now().UTC(), Props: nil,
		}
		if err := env.NodeSvc.AddRelationship(env.Ctx, relationship); err != nil {
			t.Fatalf("add %s from issue %s to member %s after activation: %v", relationType, issue.ID, memberID, err)
		}
		edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, relationType)
		if err != nil || len(edges) != 1 || edges[0].TargetID != memberID {
			t.Fatalf("%s edges of issue %s = %v, %v, want one edge to member %s", relationType, issue.ID, edges, err, memberID)
		}
	}
}

// TestNodeIntegrityStrictRejectsUnknownAssignmentAfterActivation activates
// strict validation and attempts to add assigned_to edges from an issue
// through NodeService.AddRelationship. The targets are a user outside the
// organization and a random UUID. The test checks that each call returns
// an error that wraps domain.ErrInvalidArgument and that the relationship
// store does not list an assigned_to edge for the issue.
func TestNodeIntegrityStrictRejectsUnknownAssignmentAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	outsiderID := createStrictUser(t, env)
	activateStrict(t)

	for name, targetID := range map[string]uuid.UUID{"a user outside the organization": outsiderID, "a random UUID": uuid.New()} {
		relationship := &node.Relationship{
			OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelAssignedTo, TargetID: targetID,
			CreatedBy: uuid.New(), CreatedAt: clock.Now().UTC(), Props: nil,
		}
		requireError(t, env.NodeSvc.AddRelationship(env.Ctx, relationship), domain.ErrInvalidArgument, "the assignment to "+name)
	}
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, node.RelAssignedTo)
	if err != nil || len(edges) != 0 {
		t.Fatalf("assigned_to edges of issue %s after the refused adds = %v, %v, want none", issue.ID, edges, err)
	}
}

// TestNodeIntegrityStrictRejectsUnknownAssigneeCreateAfterActivation
// verifies that NodeService.Create rejects an issue under a project when
// strict validation is active and CreateInput.Relationships assigns the issue
// to an existing user who does not belong to the test organization. The test
// requires an error that wraps domain.ErrInvalidArgument and verifies that
// the project's scope_id index does not list any issues.
func TestNodeIntegrityStrictRejectsUnknownAssigneeCreateAfterActivation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	outsiderID := createStrictUser(t, env)
	activateStrict(t)

	input := schemaCreateInput(projectID, projectID, "issue", nil)
	input.Relationships = []*node.Relationship{{
		OrgID: uuid.Nil, SourceID: uuid.Nil, RelationType: node.RelAssignedTo, TargetID: outsiderID,
		CreatedBy: uuid.Nil, CreatedAt: time.Time{}, Props: nil,
	}}
	_, err := env.NodeSvc.Create(env.Ctx, input)
	requireError(t, err, domain.ErrInvalidArgument, "the create of an issue assigned to a user outside the organization")
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("project %s lists %d issues after the refused create, want none", projectID, len(issues))
	}
}

func createStrictMember(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	userID := createStrictUser(t, env)
	if err := postgres.NewOrgMemberRepo(env.Ops.Pool).AddMember(env.Ctx, &org.Member{
		ID: uuid.Nil, OrgID: env.OrgID, UserID: userID, Role: strictMemberRole,
	}); err != nil {
		t.Fatalf("add user %s to organization %s: %v", userID, env.OrgID, err)
	}
	return userID
}

func createStrictUser(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	created, err := postgres.NewUserRepo(env.Ops.Pool).Create(env.Ctx, &user.User{
		ID: userID, Email: "strict-user-" + userID.String() + "@example.invalid", DisplayName: "Strict User", AvatarURL: nil,
	})
	if err != nil {
		t.Fatalf("create user %s: %v", userID, err)
	}
	return created.ID
}

func createStrictOtherOrg(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	if err := env.Seeder.SeedOrg(env.Ctx, orgID); err != nil {
		t.Fatalf("seed organization %s: %v", orgID, err)
	}
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{"slug": quoted("strict-other-" + orgID.String()[:8])}
	record := &node.Node{
		ID: orgID, OrgID: orgID, NodeType: "org", Name: "Strict other organization", Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: orgID, OrgID: orgID, NodeType: "org", Name: record.Name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, record, view, nil, []string{"slug"}, nil, nil); err != nil {
		t.Fatalf("create the organization node %s: %v", orgID, err)
	}
	return orgID
}
