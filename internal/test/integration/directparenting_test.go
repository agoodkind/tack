package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestMCPParentRelationshipReplacesParent moves an issue through the public
// relationship tool and rejects a cycle. The permanent ledger must record
// both parent removals and additions under each move's operation.
func TestMCPParentRelationshipReplacesParent(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores := parentIntegrityStores(t)
	child, _ := createSchemaIssue(t, harness, "relationship child")
	parent, _ := createSchemaIssue(t, harness, "relationship parent")
	replacement, _ := createSchemaIssue(t, harness, "replacement parent")
	original := storedParentView(t, stores, child.String())
	originalEdges, err := stores.Relationships.ListBySource(
		t.Context(),
		original.OrgID,
		child,
		node.RelChildOf,
	)
	if err != nil || len(originalEdges) != 1 {
		t.Fatalf("read original parent of %s: %v, %v", child, originalEdges, err)
	}

	add := schemaArguments(harness)
	add.WorkspaceReference = ""
	add.ProjectReference = ""
	add.SourceID = child.String()
	add.RelationType = node.RelChildOf
	add.TargetID = parent.String()
	harness.Call(t, "tack_add_relationship", add)
	requireAddedParent(t, child, parent)

	cycle := add
	cycle.SourceID = parent.String()
	cycle.TargetID = child.String()
	harness.CallExpectError(t, "tack_add_relationship", cycle)
	requireAddedParent(t, child, parent)

	add.TargetID = replacement.String()
	harness.Call(t, "tack_add_relationship", add)
	requireAddedParent(t, child, replacement)

	ledger := startNodeAuditLedger(t)

	for _, move := range []struct{ before, after uuid.UUID }{
		{before: originalEdges[0].TargetID, after: parent},
		{before: parent, after: replacement},
	} {
		removal := parentChangeRow(
			t,
			ledger,
			original.OrgID,
			child,
			move.before,
			audit.VerbRelationshipRemove,
		)
		addition := parentChangeRow(
			t,
			ledger,
			original.OrgID,
			child,
			move.after,
			audit.VerbRelationshipAdd,
		)
		if removal.Context.OperationID != addition.Context.OperationID ||
			removal.ActorID != addition.ActorID || removal.ActorID == uuid.Nil {
			t.Fatalf(
				"parent change has different operations or actors: %+v, %+v",
				removal,
				addition,
			)
		}

		if removal.Delta == nil || addition.Delta == nil ||
			len(removal.Delta.Before) == 0 || len(addition.Delta.After) == 0 {
			t.Fatalf(
				"parent change does not record old and new values: %+v, %+v",
				removal,
				addition,
			)
		}
	}
}

// parentChangeRow waits for the permanent relationship change for one parent.
func parentChangeRow(
	t *testing.T,
	ledger *nodeAuditLedger,
	organizationID uuid.UUID,
	child uuid.UUID,
	parent uuid.UUID,
	verb audit.Verb,
) audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		for _, row := range ledger.orgRows(t, organizationID, uuid.Nil, verb) {
			target := row.Context.Target
			if row.Context.Phase == audit.PhaseChange && target != nil &&
				target.Kind == audit.TargetKindRelationship && target.SourceID == child &&
				target.RelationType == node.RelChildOf && target.TargetID == parent {
				return row
			}
		}

		if clock.Now().After(deadline) {
			t.Fatalf("ledger does not contain %s from %s to %s", verb, child, parent)
		}

		time.Sleep(nodeAuditPollInterval)
	}
}

// requireAddedParent reads the stored child_of edges and requires one parent.
func requireAddedParent(t *testing.T, child, parent uuid.UUID) {
	t.Helper()
	stores := parentIntegrityStores(t)
	view := storedParentView(t, stores, child.String())
	edges, err := stores.Relationships.ListBySource(t.Context(), view.OrgID, child, node.RelChildOf)
	if err != nil || len(edges) != 1 || edges[0].TargetID != parent {
		t.Fatalf("parent edges of %s = %+v, %v; want one edge to %s", child, edges, err, parent)
	}
}
