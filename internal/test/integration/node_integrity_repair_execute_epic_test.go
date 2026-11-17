package integration_test

import (
	"encoding/json"
	"maps"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairExecuteEpicUnderEpic stores an epic with
// child_of edges to the project and to a second epic. The
// test freezes a repair request with --request that selects the
// second epic and removes the project edge, then executes the manifest with
// --manifest --execute. The epic must end with one child_of edge to the
// second epic, the same scope_id, name, and other properties, and the outbox
// must contain one node.update event and one relationship.remove event for
// the epic.
func TestNodeIntegrityRepairExecuteEpicUnderEpic(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, parentEpic := graph.projectID, graph.epicID
	epic := createTwoParentEpic(t, env, graph)
	stored, err := env.Stores.Views.Get(env.Ctx, epic)
	if err != nil || stored == nil {
		t.Fatalf("read epic %s: %v", epic, err)
	}

	entry := epicParentEntry("tack-582-epic-under-epic", epic, parentEpic, project)
	manifest := freezeRepairRequest(t, repairRequest(entry))
	mark := outboxTail(t, env)

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute the epic repair: %v", err)
	}

	requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyApplied)
	view := requireParent(t, env, epic, parentEpic)
	if view.Name != stored.Name {
		t.Fatalf("epic %s name = %q after the repair, want %q", epic, view.Name, stored.Name)
	}

	wantProps := maps.Clone(stored.Props)
	if !maps.EqualFunc(
		view.Props,
		wantProps,
		func(left, right json.RawMessage) bool { return string(left) == string(right) },
	) {
		t.Fatalf("epic %s properties = %v after the repair, want %v", epic, view.Props, wantProps)
	}

	if scope := propText(t, view.Props, "scope_id"); scope != project.String() {
		t.Fatalf("epic %s scope_id = %q, want project %s", epic, scope, project)
	}

	events := outboxEventsAfter(t, env, mark)

	for _, verb := range []audit.Verb{audit.VerbNodeUpdate, audit.VerbRelationshipRemove} {
		if found := changeEventsFor(events, verb, epic); len(found) != 1 {
			t.Fatalf("%s outbox events for epic %s = %d, want 1", verb, epic, len(found))
		}
	}

	requireUnchangedRerun(t, manifest)
}

// TestNodeIntegrityRepairExecuteEpicParentRefusals uses the epic fixture of
// TestNodeIntegrityRepairExecuteEpicUnderEpic. Execution must fail for a
// result selecting an issue parent, because the epic type does not list
// issue. After the test stores the earlier epic type with CanLiveUnder
// [project], execution must fail for a result selecting the second
// epic. After ops backfill once-fixed-definitions --execute, a newly frozen
// manifest with the same result must apply. Each failed execution must leave
// the epic's child_of edges unchanged.
func TestNodeIntegrityRepairExecuteEpicParentRefusals(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	project, parentEpic := graph.projectID, graph.epicID
	epic := createTwoParentEpic(t, env, graph)
	issueParent := createRepairIssue(t, env, project, "Invalid epic parent type")
	underIssue := epicParentEntry(
		"tack-582-epic-under-issue",
		epic,
		issueParent,
		project,
		parentEpic,
	)
	requireFailedEpicRepair(
		t,
		env,
		underIssue,
		epic,
		`cannot live under the parent `+issueParent.String()+` of type "issue"`,
	)

	epicType := storedNodeType(t, env, "epic")
	epicType.CanLiveUnder = []string{"project"}
	if err := env.Stores.NodeTypes.Set(env.Ctx, epicType); err != nil {
		t.Fatalf("store the earlier epic node type: %v", err)
	}

	earlier := epicParentEntry("tack-582-epic-earlier-type", epic, parentEpic, project)
	requireFailedEpicRepair(
		t,
		env,
		earlier,
		epic,
		`cannot live under the parent `+parentEpic.String()+` of type "epic"`,
	)

	runFixedDefinitions(t, "--execute")
	corrected := epicParentEntry("tack-582-epic-corrected-type", epic, parentEpic, project)
	applied, err := executeRepairManifest(t, freezeRepairRequest(t, repairRequest(corrected)))
	if err != nil {
		t.Fatalf("execute the epic repair after the correction: %v", err)
	}

	requireApplyStatus(t, applied, corrected.RepairID, ops.RepairApplyApplied)
	requireParent(t, env, epic, parentEpic)
}
