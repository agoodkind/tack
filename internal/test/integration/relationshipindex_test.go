package integration

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// TestRelationshipReverseIndex verifies the relationship store maintains the
// forward and reverse indexes in sync. Adding a relationship makes it visible
// from both source and target sides; removing it clears both directions.
func TestRelationshipReverseIndex(t *testing.T) {
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
	target := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    proj.ID,
			ScopeID:     proj.ID,
			NodeTypeKey: "issue",
			Name:        "T",
			ActorID:     actor,
		},
	)

	rel := &node.Relationship{
		OrgID:        env.OrgID,
		SourceID:     issue.ID,
		RelationType: "blocked_by",
		TargetID:     target.ID,
		CreatedBy:    actor,
	}
	if err := env.NodeSvc.AddRelationship(env.Ctx, rel); err != nil {
		t.Fatalf("AddRelationship: %v", err)
	}

	// Forward: blocked_by from issue.
	out, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListBySource: %v", err)
	}

	if len(out) != 1 || out[0].TargetID != target.ID {
		t.Fatalf("forward index missing or wrong: %+v", out)
	}

	// Reverse: blocked_by pointing at target.
	in, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, target.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListByTarget: %v", err)
	}

	if len(in) != 1 || in[0].SourceID != issue.ID {
		t.Fatalf("reverse index missing or wrong: %+v", in)
	}

	if err := env.NodeSvc.RemoveRelationship(
		env.Ctx,
		env.OrgID,
		issue.ID,
		"blocked_by",
		target.ID,
	); err != nil {
		t.Fatalf("RemoveRelationship: %v", err)
	}

	out, err = env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, issue.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListBySource after removing relationship from %s: %v", issue.ID, err)
	}

	in, err = env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, target.ID, "blocked_by")
	if err != nil {
		t.Fatalf("ListByTarget after removing relationship to %s: %v", target.ID, err)
	}

	if len(out) != 0 || len(in) != 0 {
		t.Fatalf("relationship not fully cleared: forward=%v reverse=%v", out, in)
	}
}
