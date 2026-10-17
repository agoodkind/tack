package integration_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentEdgeGuardKeepsCanonicalEdge creates a parent issue and a child
// issue under it through the node service.
// The child issue stores the parent issue's UUID as parent_id.
// The relationship store adds a redundant child_of edge from the child issue
// to its project, an ancestor of the child.
// The test checks that the relationship store refuses removal of the canonical
// edge from the child issue to the parent issue with an error that matches
// domain.ErrFailedPrecondition and retains both edges.
// The test then checks that the relationship store removes the redundant edge
// and retains only the canonical edge.
func TestParentEdgeGuardKeepsCanonicalEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "GUARD")
	parent := createDeleteFixtureNode(t, env, actor, project, project, "issue", "Parent issue", nil)
	child := createDeleteFixtureNode(t, env, actor, parent, project, "issue", "Child issue", nil)
	redundant := &node.Relationship{OrgID: env.OrgID, SourceID: child, RelationType: node.RelChildOf, TargetID: project}
	if err := env.Stores.Relationships.Add(env.Ctx, redundant); err != nil {
		t.Fatalf("add redundant parent edge of issue %s: %v", child, err)
	}
	requireRemovalRefused(t, env, child, parent)
	requireParentEdges(t, env, child, parent, project)
	if err := env.Stores.Relationships.Remove(env.Ctx, env.OrgID, child, node.RelChildOf, project); err != nil {
		t.Fatalf("remove redundant parent edge of issue %s: %v", child, err)
	}
	requireParentEdges(t, env, child, parent)
}

// TestParentEdgeGuardRefusesRootTypeCanonicalEdge stores a node type that
// does not declare CanLiveUnder types.
// The test creates a node of that type under a project through the node
// service.
// The node stores the project's UUID as parent_id.
// The test checks that the relationship store refuses removal of the node's
// only child_of edge with an error that matches domain.ErrFailedPrecondition
// and retains the edge.
func TestParentEdgeGuardRefusesRootTypeCanonicalEdge(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	project := createDeleteFixtureProject(t, env, actor, "GUARDROOT")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: "guardroot", TypeKey: "guardroot",
		Slug: "guardroot", PluralSlug: "guardroots",
	}
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("store node type %s: %v", kind.TypeKey, err)
	}
	rooted := createDeleteFixtureNode(t, env, actor, project, project, kind.TypeKey, "Node of a root type", nil)
	requireRemovalRefused(t, env, rooted, project)
	requireParentEdges(t, env, rooted, project)
}

func requireRemovalRefused(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID) {
	t.Helper()
	err := env.Stores.Relationships.Remove(env.Ctx, env.OrgID, sourceID, node.RelChildOf, targetID)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("removal of the canonical parent edge from %s to %s returned %v, want a refusal", sourceID, targetID, err)
	}
}

func requireParentEdges(t *testing.T, env *integration.TestEnv, sourceID uuid.UUID, want ...uuid.UUID) {
	t.Helper()
	edges, err := env.Stores.Relationships.EdgesFrom(env.Ctx, env.OrgID, sourceID, node.RelChildOf, "", 10)
	if err != nil {
		t.Fatalf("read child_of edges of %s: %v", sourceID, err)
	}
	got := slices.Clone(edges.IDs)
	expected := slices.Clone(want)
	byText := func(left, right uuid.UUID) int { return strings.Compare(left.String(), right.String()) }
	slices.SortFunc(got, byText)
	slices.SortFunc(expected, byText)
	if !slices.Equal(got, expected) {
		t.Fatalf("child_of edges of %s = %v, want %v", sourceID, got, expected)
	}
}
