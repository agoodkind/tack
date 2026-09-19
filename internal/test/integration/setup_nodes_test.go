package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

// createTestProject creates a workspace and a project under env's org and
// returns the project ID.
func createTestProject(t *testing.T, env *TestEnv) uuid.UUID {
	t.Helper()
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Paged", workspace.ID, workspace.ID, actor)
	return project.ID
}

// createTestIssue creates one issue directly under projectID.
func createTestIssue(t *testing.T, env *TestEnv, projectID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	issue := mustCreate(t, env, service.CreateInput{
		ParentID: projectID, ScopeID: projectID, NodeTypeKey: "issue", Name: name, ActorID: uuid.New(),
	})
	return issue.ID
}
