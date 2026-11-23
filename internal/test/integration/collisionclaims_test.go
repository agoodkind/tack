package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func TestScopeRewriteCannotProduceADuplicate(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	residentProject := mustCreateScope(
		t,
		env,
		"project",
		"Resident",
		workspace.ID,
		workspace.ID,
		actor,
	)
	migrantProject := mustCreateScope(
		t,
		env,
		"project",
		"Migrant",
		workspace.ID,
		workspace.ID,
		actor,
	)
	resident := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    residentProject.ID,
			ScopeID:     residentProject.ID,
			NodeTypeKey: "issue",
			Name:        "Resident",
			ActorID:     actor,
		},
	)
	migrant := mustCreate(
		t,
		env,
		service.CreateInput{
			ParentID:    migrantProject.ID,
			ScopeID:     migrantProject.ID,
			NodeTypeKey: "issue",
			Name:        "Migrant",
			ActorID:     actor,
		},
	)
	if numberPropOf(t, resident, "sequence") != 1 || numberPropOf(t, migrant, "sequence") != 1 {
		t.Fatalf(
			"sequences = %d, %d, want 1 in each project",
			numberPropOf(t, resident, "sequence"),
			numberPropOf(t, migrant, "sequence"),
		)
	}

	migrantNode, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, migrant.ID)
	if err != nil || migrantNode == nil {
		t.Fatalf("get migrant: %v", err)
	}

	oldProps := maps.Clone(migrantNode.Props)
	migrantNode.Props["scope_id"] = jsonStr(residentProject.ID.String())
	migrantNode.Props["parent_id"] = jsonStr(residentProject.ID.String())
	residentKey := seededReferenceKey(t, env, resident, stringPropOf(residentProject, "identifier"))
	err = env.Stores.Nodes.UpdateAtomic(
		env.Ctx,
		migrantNode,
		referenceTestView(migrantNode),
		oldProps,
		nil,
		[]node.ReferenceKey{residentKey},
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpdateAtomic error = %v, want domain.ErrConflict", err)
	}

	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, migrant.ID)
	if err != nil {
		t.Fatalf("get migrant after rejected update: %v", err)
	}

	if stored == nil ||
		!maps.EqualFunc(
			stored.Props,
			oldProps,
			func(first, second json.RawMessage) bool {
				return bytes.Equal(first, second)
			},
		) {
		t.Fatalf("migrant props = %#v, want unchanged %#v", stored, oldProps)
	}
}
