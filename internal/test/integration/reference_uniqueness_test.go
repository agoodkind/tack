package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
)

func TestCreateAtomicRejectsDuplicateReference(t *testing.T) {
	env := SetupTestEnv(t)
	first := referenceTestNode(env.OrgID)
	firstView := referenceTestView(first)
	key := node.ReferenceKey{TemplateName: "reference", Encoded: "taken"}
	if err := env.Stores.Nodes.CreateAtomic(
		env.Ctx,
		first,
		firstView,
		nil,
		nil,
		[]node.ReferenceKey{key},
		nil,
	); err != nil {
		t.Fatalf("create first node: %v", err)
	}

	second := referenceTestNode(env.OrgID)
	if err := env.Stores.Nodes.CreateAtomic(
		env.Ctx,
		second,
		referenceTestView(second),
		nil,
		nil,
		[]node.ReferenceKey{key},
		nil,
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create duplicate node error = %v, want ErrConflict", err)
	}

	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, second.ID)
	if err != nil {
		t.Fatalf("get rejected node: %v", err)
	}

	if stored != nil {
		t.Fatalf("rejected node = %+v, want nil", stored)
	}

	missing, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "missing")
	if err != nil {
		t.Fatalf("lookup missing reference: %v", err)
	}

	if missing != uuid.Nil {
		t.Fatalf("missing reference node ID = %s, want nil UUID", missing)
	}
}

func TestCrossTypeReferenceCollisionIsRejected(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	epic := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    project.ID,
			ScopeID:     project.ID,
			NodeTypeKey: "epic",
			Name:        "Epic",
			ActorID:     actor,
		},
	)
	issue := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    project.ID,
			ScopeID:     project.ID,
			NodeTypeKey: "issue",
			Name:        "Issue",
			ActorID:     actor,
		},
	)

	epicSequence := numberPropOf(t, epic, "sequence")
	issueSequence := numberPropOf(t, issue, "sequence")
	if issueSequence != epicSequence+1 {
		t.Fatalf("sequences = %d, %d, want consecutive shared values", epicSequence, issueSequence)
	}

	scopeReference := stringPropOf(project, "identifier")
	if seededReferenceKey(t, env, epic, scopeReference).Encoded == seededReferenceKey(
		t,
		env,
		issue,
		scopeReference,
	).Encoded {
		t.Fatal("epic and issue rendered the same reference")
	}

	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}

	if len(duplicates) != 0 {
		t.Fatalf("duplicate groups = %d, want 0; groups = %+v", len(duplicates), duplicates)
	}
}
