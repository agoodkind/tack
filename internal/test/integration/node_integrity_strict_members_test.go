package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/test/integration"
)

// strictMemberRole is the organization member role that the test assigns.
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

// createStrictMember creates a user and adds the user to the test
// organization through the YugabyteDB repositories. It returns the user ID.
func createStrictMember(t *testing.T, env *integration.TestEnv) uuid.UUID {
	t.Helper()
	userID := uuid.Must(uuid.NewV7())
	created, err := postgres.NewUserRepo(env.Ops.Pool).Create(env.Ctx, &user.User{
		ID: userID, Email: "strict-member-" + userID.String() + "@example.invalid", DisplayName: "Strict Member", AvatarURL: nil,
	})
	if err != nil {
		t.Fatalf("create user %s: %v", userID, err)
	}
	if err := postgres.NewOrgMemberRepo(env.Ops.Pool).AddMember(env.Ctx, &org.Member{
		ID: uuid.Nil, OrgID: env.OrgID, UserID: created.ID, Role: strictMemberRole,
	}); err != nil {
		t.Fatalf("add user %s to organization %s: %v", created.ID, env.OrgID, err)
	}
	return created.ID
}

// createStrictOtherOrg seeds a second organization and writes its
// organization node through NodeStore.CreateAtomic. It returns the
// organization ID, which is also the ID of the organization node.
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
