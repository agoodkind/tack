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
