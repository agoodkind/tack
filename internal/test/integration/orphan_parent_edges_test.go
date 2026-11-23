package integration

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
)

// TestOrphanBackfillRefusesAmbiguousParentEdges requires an explicit repair
// when an issue has several existing child_of targets. Dry runs and executed
// runs must refuse the ambiguity and preserve every node and relationship.
func TestOrphanBackfillRefusesAmbiguousParentEdges(t *testing.T) {
	env := SetupTestEnv(t)
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Edges", workspace.ID, workspace.ID, actor)
	issueID := createTestIssue(t, env, project.ID, "Issue with extra parents")
	kept := []uuid.UUID{issueID, project.ID}

	for _, typeKey := range []string{"cycle", "module", "epic"} {
		container := mustCreate(
			t,
			env,
			service.CreateInput{
				ParentID:    project.ID,
				ScopeID:     project.ID,
				NodeTypeKey: typeKey,
				Name:        "Container " + typeKey,
				ActorID:     actor,
			},
		)
		kept = append(kept, container.ID)
		if err := env.Stores.Relationships.Add(
			env.Ctx,
			&node.Relationship{
				OrgID:        env.OrgID,
				SourceID:     issueID,
				RelationType: node.RelChildOf,
				TargetID:     container.ID,
				CreatedBy:    actor,
				CreatedAt:    time.Now().UTC(),
				Props:        nil,
			},
		); err != nil {
			t.Fatalf("add child_of edge to %s: %v", typeKey, err)
		}
	}

	requireParentEdges(t, env, issueID, 4)
	drainOutbox(t, env)
	principal := audit.OperatorPrincipal{
		ID:     uuid.New(),
		Email:  "operator@example.com",
		Name:   "Operator",
		Source: "test",
	}
	ctx := audit.WithOpID(
		audit.WithOperatorPrincipal(t.Context(), principal),
		uuid.Must(uuid.NewV7()),
	)

	before, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		issueID,
		node.RelChildOf,
	)
	if err != nil {
		t.Fatalf("read parent edges of issue %s: %v", issueID, err)
	}

	for _, dryRun := range []bool{true, false} {
		result, err := ops.RunOrphanNodeBackfill(
			ctx,
			env.Stores.NodeDeleter,
			env.Stores.Relationships,
			env.NodeSvc,
			dryRun,
		)
		if !errors.Is(err, node.ErrNoHierarchyParent) ||
			!strings.Contains(err.Error(), issueID.String()) {
			t.Fatalf(
				"orphan backfill dry_run=%t returned %+v, %v, want an explicit parent ambiguity refusal for %s",
				dryRun,
				result,
				err,
				issueID,
			)
		}

		if result.RemovedEdges != 0 || result.Deleted != 0 {
			t.Fatalf("refused backfill changed records: %+v", result)
		}

		requireNodesPresent(t, env, kept)
		after, readErr := env.Stores.Relationships.ListBySource(
			env.Ctx,
			env.OrgID,
			issueID,
			node.RelChildOf,
		)
		if readErr != nil || len(after) != len(before) {
			t.Fatalf("read preserved parent edges: %v, %v", after, readErr)
		}

		for index, edge := range before {
			if !reflect.DeepEqual(after[index], edge) {
				t.Fatalf("parent edge changed from %+v to %+v", edge, after[index])
			}
		}
	}

	if events := outboxEvents(t, env); len(events) != 0 {
		t.Fatalf("refused backfill wrote change events: %+v", events)
	}
}

// requireParentEdges requires nodeID to have want child_of edges.
func requireParentEdges(t *testing.T, env *TestEnv, nodeID uuid.UUID, want int) {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, nodeID, node.RelChildOf)
	if err != nil {
		t.Fatalf("list child_of edges of node %s: %v", nodeID, err)
	}

	if len(edges) != want {
		t.Fatalf("node %s has %d child_of edges, want %d", nodeID, len(edges), want)
	}
}
