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

// TestParentEdgeGuardKeepsLastEdge checks that the guard permits removal
// of one parent edge and refuses removal of the remaining edge.
func TestParentEdgeGuardKeepsLastEdge(t *testing.T) {
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
	requireRemovalRefused(t, env, child, parent)
}

// TestParentEdgeGuardAllowsRootTypeWithoutParent checks that a node type
// without CanLiveUnder types permits removal of its final parent edge.
func TestParentEdgeGuardAllowsRootTypeWithoutParent(t *testing.T) {
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
	kind.CanLiveUnder = nil
	if err := env.Stores.NodeTypes.Set(env.Ctx, kind); err != nil {
		t.Fatalf("remove parent metadata from type %s: %v", kind.TypeKey, err)
	}

	if err := env.Stores.Relationships.Remove(
		env.Ctx,
		env.OrgID,
		rooted,
		node.RelChildOf,
		project,
	); err != nil {
		t.Fatalf("remove parent edge of root type node %s: %v", rooted, err)
	}

	requireParentEdges(t, env, rooted)
}

// storeGuardRootType permits creation under a project before the test removes
// that parent declaration.
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

func requireRemovalRefused(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID) {
	t.Helper()
	err := env.Stores.Relationships.Remove(env.Ctx, env.OrgID, sourceID, node.RelChildOf, targetID)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf(
			"removal of the canonical parent edge from %s to %s returned %v, want a refusal",
			sourceID,
			targetID,
			err,
		)
	}
}

func requireParentEdges(
	t *testing.T,
	env *integration.TestEnv,
	sourceID uuid.UUID,
	want ...uuid.UUID,
) {
	t.Helper()
	edges, err := env.Stores.Relationships.EdgesFrom(
		env.Ctx,
		env.OrgID,
		sourceID,
		node.RelChildOf,
		"",
		10,
	)
	if err != nil {
		t.Fatalf("read child_of edges of %s: %v", sourceID, err)
	}

	got := slices.Clone(edges.IDs)
	expected := slices.Clone(want)
	byText := func(left, right uuid.UUID) int {
		return strings.Compare(left.String(), right.String())
	}
	slices.SortFunc(got, byText)
	slices.SortFunc(expected, byText)
	if !slices.Equal(got, expected) {
		t.Fatalf("child_of edges of %s = %v, want %v", sourceID, got, expected)
	}
}
