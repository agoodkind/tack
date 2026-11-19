package integration_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

func childOfTargets(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) []uuid.UUID {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges of %s: %v", nodeID, err)
	}

	targets := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		targets = append(targets, edge.TargetID)
	}

	slices.SortFunc(
		targets,
		func(left, right uuid.UUID) int { return strings.Compare(left.String(), right.String()) },
	)

	return targets
}

// requireParent checks that nodeID has parentID as parent_id and exactly one
// child_of edge, which targets parentID.
func requireParent(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	parentID uuid.UUID,
) *node.NodeView {
	t.Helper()
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read view %s: %v", nodeID, err)
	}

	if got := propText(t, view.Props, "parent_id"); got != parentID.String() {
		t.Fatalf("node %s parent_id = %q, want %s", nodeID, got, parentID)
	}

	if targets := childOfTargets(t, env, nodeID); len(targets) != 1 || targets[0] != parentID {
		t.Fatalf("node %s child_of targets = %v, want only %s", nodeID, targets, parentID)
	}

	return view
}

// projectState returns the ID of the default state in projectID with the given
// name.
func projectState(
	t *testing.T,
	env *integration.TestEnv,
	projectID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()
	edges, err := env.Stores.Relationships.ListByTarget(
		env.Ctx,
		env.OrgID,
		projectID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("list children of project %s: %v", projectID, err)
	}

	for _, edge := range edges {
		view, err := env.Stores.Views.Get(env.Ctx, edge.SourceID)
		if err == nil && view != nil && view.NodeType == "state" && view.Name == name {
			return view.ID
		}
	}

	t.Fatalf("project %s has no state %q", projectID, name)

	return uuid.Nil
}

func commentsUnder(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) []*node.NodeView {
	t.Helper()
	edges, err := env.Stores.Relationships.ListByTarget(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list children of %s: %v", nodeID, err)
	}

	comments := []*node.NodeView{}

	for _, edge := range edges {
		view, err := env.Stores.Views.Get(env.Ctx, edge.SourceID)
		if err == nil && view != nil && view.NodeType == "comment" {
			comments = append(comments, view)
		}
	}

	return comments
}
