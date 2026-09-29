package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchRebuildExcludesOrphanNodes runs a full FoundationDB replacement
// over two nodes without a hierarchy parent. Each copy fails access
// compilation. The replacement must still finish and move the public alias,
// and ops search verify must list both nodes. The test then deletes one
// orphan and adds a parent to the other. The delete must remove the first
// exclusion. The new parent must index the second node in the new serving
// index, make it searchable, and remove its exclusion.
func TestSearchRebuildExcludesOrphanNodes(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	steady := putOpaqueNode(t, fixture, kind, entry, "steady node", "amber orphan steady", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	orphan := putOrphanNode(t, kind, "orphan node", "amber orphan repaired")
	deleted := putOrphanNode(t, kind, "deleted orphan", "amber orphan deleted")

	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test"}
	rebuild := beginRebuild(t, fixture, request)
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, rebuild.TargetIndex, []uuid.UUID{steady})

	report, verifyErr := runSearchVerifyCommand(t, fixture.Config)
	t.Logf("ops search verify physical checks: %v", verifyErr)
	for _, nodeID := range []uuid.UUID{orphan, deleted} {
		reason, listed := report.reason(nodeID)
		if !listed || !strings.Contains(reason, "exactly one hierarchy parent") {
			t.Fatalf("ops search verify listed %+v, want node %s with its hierarchy failure", report, nodeID)
		}
		requireDeletedAbsent(t, fixture, rebuild.TargetIndex, nodeID)
	}
	for _, exclusion := range report.Exclusions {
		if exclusion.Class != string(searchdomain.WorkClassCopy) || exclusion.Index != rebuild.TargetIndex {
			t.Fatalf("exclusion of node %s names %s work on %s, want copy work on %s", exclusion.NodeID, exclusion.Class, exclusion.Index, rebuild.TargetIndex)
		}
	}
	if report.ExcludedNodes != len(report.Exclusions) || report.ExcludedNodes != 2 {
		t.Fatalf("ops search verify counted %d exclusions and listed %d, want 2", report.ExcludedNodes, len(report.Exclusions))
	}

	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete orphan node: %v", err)
	}
	parent := &node.Relationship{OrgID: kind.OrgID, SourceID: orphan, TargetID: entry, RelationType: node.RelChildOf}
	if err := fixture.Stores.Relationships.Add(t.Context(), parent); err != nil {
		t.Fatalf("add parent of orphan node %s: %v", orphan, err)
	}
	drainSearchWork(t, fixture.Worker, 2000)

	requireCurrentPages(t, fixture, rebuild.TargetIndex, []uuid.UUID{orphan})
	results := callEverySearchPage(t, "amber orphan repaired", fixture.Harness)
	if !slices.Contains(results.IDs, orphan) {
		t.Fatalf("search after the repair returned %v, want node %s", results.IDs, orphan)
	}
	report, _ = runSearchVerifyCommand(t, fixture.Config)
	if report.ExcludedNodes != 0 {
		t.Fatalf("ops search verify still lists %+v after the repair and the delete", report.Exclusions)
	}
}

// TestSearchExcludesNodeAfterAttemptLimit stores a node with a value under a
// property that has no definition. Every live slice of the node fails to
// encode its text. After the attempt limit ops search verify must list the
// node with that failure in the live class. The test then declares the
// property. The declaration must index the node and remove its exclusion.
func TestSearchExcludesNodeAfterAttemptLimit(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	drainSearchWork(t, fixture.Worker, 2000)

	name := opaqueSearchKey("p")
	affected := putUndeclaredNode(t, fixture, kind, entry, name, "quartz lantern harbor")
	report := runUntilExcluded(t, fixture, fixture.Worker, affected)
	reason, _ := report.reason(affected)
	if !strings.Contains(reason, name) {
		t.Fatalf("exclusion reason %q does not name the undeclared property %s", reason, name)
	}
	for _, exclusion := range report.Exclusions {
		if exclusion.NodeID == affected.String() && exclusion.Class != string(searchdomain.WorkClassLive) {
			t.Fatalf("exclusion class = %q, want %q", exclusion.Class, searchdomain.WorkClassLive)
		}
	}
	drainSearchWork(t, fixture.Worker, 2000)

	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: name, Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: 2, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := fixture.Stores.PropertyDefs.Set(t.Context(), definition); err != nil {
		t.Fatalf("declare property %s: %v", name, err)
	}
	drainSearchWork(t, fixture.Worker, 2000)
	if pages := searchNodePages(t, fixture.Client, fixture.Index, affected, false); len(pages) == 0 {
		t.Fatal("the excluded node has no pages after its property was declared")
	}
	report, _ = runSearchVerifyCommand(t, fixture.Config)
	if _, listed := report.reason(affected); listed {
		t.Fatalf("ops search verify still lists node %s after the repair", affected)
	}
}
