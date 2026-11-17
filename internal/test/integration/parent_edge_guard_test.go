package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeGuardKeepsCanonicalEdge checks that the guard refuses removal
// of the child issue's canonical parent edge, which targets parent_id, while
// a redundant child_of edge to an ancestor exists, and allows removal of the
// redundant edge.
func TestParentEdgeGuardKeepsCanonicalEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "GUARD")
	parent := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Parent issue", nil)
	child := createDeleteFixtureNode(t, env, actor, parent, project, "issue", "Child issue", nil)
	redundant := &node.Relationship{
		OrgID:        env.OrgID,
		SourceID:     child,
		RelationType: node.RelChildOf,
		TargetID:     project,
	}
	if err := env.Stores.Relationships.Add(env.Ctx, redundant); err != nil {
		t.Fatalf("add redundant parent edge of issue %s: %v", child, err)
	}

	requireRemovalRefused(t, env, child, parent)
	requireParentEdges(t, env, child, parent, project)
	if err := env.Stores.Relationships.Remove(
		env.Ctx,
		env.OrgID,
		child,
		node.RelChildOf,
		project,
	); err != nil {
		t.Fatalf("remove redundant parent edge of issue %s: %v", child, err)
	}

	requireParentEdges(t, env, child, parent)
}

// TestParentEdgeGuardRefusesRootTypeCanonicalEdge checks that the guard
// refuses removal of the only child_of edge of a node with parent_id when the
// node type does not declare CanLiveUnder types.
func TestParentEdgeGuardRefusesRootTypeCanonicalEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "GUARDROOT")
	kind := storeGuardRootType(t, env)
	rooted := createDeleteFixtureNode(
		t,
		env,
		actor,
		project,
		project,
		kind.TypeKey,
		"Node of a root type",
		nil,
	)
	clearGuardRootParentTypes(t, env, kind)
	requireRemovalRefused(t, env, rooted, project)
	requireParentEdges(t, env, rooted, project)
}

// TestParentEdgeGuardRefusesInvalidParentID creates a node under a project,
// then clears its type's CanLiveUnder before storing an invalid parent_id.
// It requires removal of the only child_of edge to fail with an error that
// matches domain.ErrFailedPrecondition and requires the edge to remain.
func TestParentEdgeGuardRefusesInvalidParentID(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "GUARDINVALID")
	kind := storeGuardRootType(t, env)
	rooted := createDeleteFixtureNode(
		t,
		env,
		actor,
		project,
		project,
		kind.TypeKey,
		"Node with an invalid parent",
		nil,
	)
	clearGuardRootParentTypes(t, env, kind)
	storeParentID(t, env, rooted, "not a UUID")
	requireRemovalRefused(t, env, rooted, project)
	requireParentEdges(t, env, rooted, project)
}

// storeGuardRootType permits project parents while the test creates its node.
func storeGuardRootType(t *testing.T, env *integration.TestEnv) *node.NodeType {
	t.Helper()
	kind := &node.NodeType{
		ID:           uuid.Must(uuid.NewV7()),
		OrgID:        env.OrgID,
		Name:         "guardroot",
		TypeKey:      "guardroot",
		Slug:         "guardroot",
		PluralSlug:   "guardroots",
		CanLiveUnder: []string{"project"},
	}
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("store node type %s: %v", kind.TypeKey, err)
	}

	return kind
}

func clearGuardRootParentTypes(t *testing.T, env *integration.TestEnv, kind *node.NodeType) {
	t.Helper()
	kind.CanLiveUnder = nil
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("clear allowed parent types for %s: %v", kind.TypeKey, err)
	}
}
