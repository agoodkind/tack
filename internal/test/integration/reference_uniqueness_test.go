package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
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
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, first, firstView, nil, nil, []node.ReferenceKey{key}, nil); err != nil {
		t.Fatalf("create first node: %v", err)
	}
	second := referenceTestNode(env.OrgID)
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, second, referenceTestView(second), nil, nil, []node.ReferenceKey{key}, nil); !errors.Is(err, domain.ErrConflict) {
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

func TestAllocateSequenceByKeyReturnsConsecutiveValues(t *testing.T) {
	env := SetupTestEnv(t)
	for want := int64(1); want <= 3; want++ {
		got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
		if err != nil {
			t.Fatalf("AllocateSequenceByKey: %v", err)
		}
		if got != want {
			t.Fatalf("sequence = %d, want %d", got, want)
		}
	}
}

func TestAllocateSequenceByKeySeparatesKeys(t *testing.T) {
	env := SetupTestEnv(t)
	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-issue-")
	if err != nil {
		t.Fatalf("allocate issue sequence: %v", err)
	}
	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-epic-")
	if err != nil {
		t.Fatalf("allocate epic sequence: %v", err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("sequences = %d, %d, want independent first values", first, second)
	}
}

func TestSeedSequenceByKeyRaisesCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("SeedSequenceByKey: %v", err)
	}
	got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("AllocateSequenceByKey: %v", err)
	}
	if got != 41 {
		t.Fatalf("sequence = %d, want 41", got)
	}
}

func TestSeedSequenceByKeyNeverLowersCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("seed counter to 40: %v", err)
	}
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 10); err != nil {
		t.Fatalf("seed counter to 10: %v", err)
	}
	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if first != 41 || second != 42 {
		t.Fatalf("sequences = %d, %d, want 41, 42", first, second)
	}
}

func TestCrossTypeReferenceCollisionIsRejected(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	epic := mustCreate(t, env, service.CreateInput{ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "epic", Name: "Epic", ActorID: actor})
	issue := mustCreate(t, env, service.CreateInput{ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Issue", ActorID: actor})

	epicSequence := numberPropOf(t, epic, "sequence")
	issueSequence := numberPropOf(t, issue, "sequence")
	if issueSequence != epicSequence+1 {
		t.Fatalf("sequences = %d, %d, want consecutive shared values", epicSequence, issueSequence)
	}
	scopeReference := stringPropOf(project, "identifier")
	if seededReferenceKey(t, env, epic, scopeReference).Encoded == seededReferenceKey(t, env, issue, scopeReference).Encoded {
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

func TestScopeRewriteCannotProduceADuplicate(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	residentProject := mustCreateScope(t, env, "project", "Resident", workspace.ID, workspace.ID, actor)
	migrantProject := mustCreateScope(t, env, "project", "Migrant", workspace.ID, workspace.ID, actor)
	resident := mustCreate(t, env, service.CreateInput{ParentID: residentProject.ID, ScopeID: residentProject.ID, NodeTypeKey: "issue", Name: "Resident", ActorID: actor})
	migrant := mustCreate(t, env, service.CreateInput{ParentID: migrantProject.ID, ScopeID: migrantProject.ID, NodeTypeKey: "issue", Name: "Migrant", ActorID: actor})
	if numberPropOf(t, resident, "sequence") != 1 || numberPropOf(t, migrant, "sequence") != 1 {
		t.Fatalf("sequences = %d, %d, want 1 in each project", numberPropOf(t, resident, "sequence"), numberPropOf(t, migrant, "sequence"))
	}
	migrantNode, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, migrant.ID)
	if err != nil || migrantNode == nil {
		t.Fatalf("get migrant: %v", err)
	}
	oldProps := maps.Clone(migrantNode.Props)
	migrantNode.Props["scope_id"] = jsonStr(residentProject.ID.String())
	migrantNode.Props["parent_id"] = jsonStr(residentProject.ID.String())
	residentKey := seededReferenceKey(t, env, resident, stringPropOf(residentProject, "identifier"))
	err = env.Stores.Nodes.UpdateAtomic(env.Ctx, migrantNode, referenceTestView(migrantNode), oldProps, nil, []node.ReferenceKey{residentKey})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpdateAtomic error = %v, want domain.ErrConflict", err)
	}
	stored, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, migrant.ID)
	if err != nil {
		t.Fatalf("get migrant after rejected update: %v", err)
	}
	if stored == nil || !maps.EqualFunc(stored.Props, oldProps, func(first, second json.RawMessage) bool {
		return bytes.Equal(first, second)
	}) {
		t.Fatalf("migrant props = %#v, want unchanged %#v", stored, oldProps)
	}
}

func seededReferenceKey(t *testing.T, env *TestEnv, current *node.NodeView, scopeReference string) node.ReferenceKey {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}
	for _, nodeType := range types {
		if nodeType.TypeKey != current.NodeType {
			continue
		}
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil {
			t.Fatalf("node type %q has no primary reference template", current.NodeType)
		}
		encoded, renderErr := template.Render(node.ReferenceRenderInput{NodeTypeKey: current.NodeType, Props: current.Props, ScopeRefs: map[string]string{node.FeatureIsScope: scopeReference}})
		if renderErr != nil {
			t.Fatalf("render reference for %s: %v", current.ID, renderErr)
		}
		return node.ReferenceKey{TemplateName: template.Name, Encoded: encoded}
	}
	t.Fatalf("node type %q not seeded", current.NodeType)
	return node.ReferenceKey{}
}

func referenceTestNode(orgID uuid.UUID) *node.Node {
	now := clock.Now().UTC()
	return &node.Node{ID: uuid.New(), OrgID: orgID, NodeType: "test", Name: "Test", CreatedAt: now, UpdatedAt: now}
}

func referenceTestView(current *node.Node) *node.NodeView {
	return &node.NodeView{ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt}
}
