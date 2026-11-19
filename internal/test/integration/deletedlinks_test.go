package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestDeleteClearsRelationships deletes the source of a blocks edge
// and checks that neither its source nor its target lists the edge.
func TestDeleteClearsRelationships(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	ws := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	proj := mustCreateScope(t, env, "project", "Tack", ws.ID, ws.ID, actor)
	issue := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "I",
			ActorID:     actor,
		},
	)
	other := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "O",
			ActorID:     actor,
		},
	)

	if err := env.NodeSvc.AddRelationship(
		env.Ctx,
		&node.Relationship{
			OrgID:        env.OrgID,
			SourceID:     issue.ID,
			RelationType: "blocks",
			TargetID:     other.ID,
			CreatedBy:    actor,
		},
	); err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}

	if _, err := env.NodeSvc.Delete(env.Ctx, issue.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	out, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "")
	if err != nil {
		t.Fatalf("ListBySource after deleting node %s: %v", issue.ID, err)
	}

	in, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, other.ID, "")
	if err != nil {
		t.Fatalf(
			"ListByTarget for surviving node %s after deleting node %s: %v",
			other.ID,
			issue.ID,
			err,
		)
	}

	if len(out) != 0 {
		t.Errorf("forward edges from deleted node should be gone: %v", out)
	}

	if len(in) != 0 {
		t.Errorf("reverse edges to surviving node should be gone: %v", in)
	}
}
