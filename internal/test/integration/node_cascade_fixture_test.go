package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// cascadeIssueCount and cascadeCommentsPerIssue size the project that the
// cascade tests delete.
const (
	cascadeIssueCount       = 3
	cascadeCommentsPerIssue = 2
)

// cascadeFixture is one project with its descendants and the nodes outside
// the project that a delete of the project must keep.
type cascadeFixture struct {
	Project     uuid.UUID
	Descendants []uuid.UUID
	Kept        []uuid.UUID
}

// newCascadeFixture creates a workspace with two projects and a label. The
// first project contains its default states, an epic, an issue under the
// epic, issues directly under the project, and comments under every issue.
// Every issue is labeled with the workspace label. The second project, the
// workspace, and the label are outside the first project.
func newCascadeFixture(t *testing.T, env *TestEnv) cascadeFixture {
	t.Helper()
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Doomed", workspace.ID, workspace.ID, actor)
	other := mustCreateScope(t, env, "project", "Kept", workspace.ID, workspace.ID, actor)
	label := mustCreate(t, env, service.CreateInput{
		ParentID: workspace.ID, ScopeID: workspace.ID, NodeTypeKey: "label", Name: "shared", ActorID: actor,
	})
	fixture := cascadeFixture{
		Project:     project.ID,
		Descendants: []uuid.UUID{},
		Kept:        []uuid.UUID{workspace.ID, other.ID, label.ID},
	}
	states, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", jsonStr(project.ID.String()))
	if err != nil {
		t.Fatalf("list states of project %s: %v", project.ID, err)
	}
	if len(states) == 0 {
		t.Fatalf("project %s has no default states", project.ID)
	}
	for _, state := range states {
		fixture.Descendants = append(fixture.Descendants, state.ID)
	}
	epic := mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic", ActorID: actor,
	})
	fixture.Descendants = append(fixture.Descendants, epic.ID)
	parents := []uuid.UUID{epic.ID}
	for range cascadeIssueCount {
		parents = append(parents, project.ID)
	}
	for position, parentID := range parents {
		issue := mustCreate(t, env, service.CreateInput{
			ParentID: parentID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue " + string(rune('A'+position)), ActorID: actor,
		})
		fixture.Descendants = append(fixture.Descendants, issue.ID)
		if err := env.NodeSvc.AddRelationship(env.Ctx, &node.Relationship{
			OrgID: env.OrgID, SourceID: issue.ID, RelationType: node.RelLabeledWith, TargetID: label.ID, CreatedBy: actor,
		}); err != nil {
			t.Fatalf("label issue %s: %v", issue.ID, err)
		}
		for range cascadeCommentsPerIssue {
			comment := mustCreate(t, env, service.CreateInput{
				ParentID: issue.ID, ScopeID: project.ID, NodeTypeKey: "comment", Name: "Comment", ActorID: actor,
			})
			fixture.Descendants = append(fixture.Descendants, comment.ID)
		}
	}
	return fixture
}
