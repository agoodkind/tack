package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
)

func TestRepairReferenceUniquenessSeedsHighestGeneratedValue(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	mustCreateLegacyRepairReference(t, env, "issue", "One", project.ID, 1)
	mustCreateLegacyRepairReference(t, env, "issue", "Two", project.ID, 2)
	mustCreateLegacyRepairReference(t, env, "issue", "Seven", project.ID, 7)

	if _, err := ops.RepairReferenceUniqueness(
		env.Ctx, env.Ops, ops.RepairReferenceOptions{Execute: true},
	); err != nil {
		t.Fatalf("RepairReferenceUniqueness: %v", err)
	}
	next, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("AllocateSequenceByKey: %v", err)
	}
	if next != 8 {
		t.Fatalf("next sequence = %d, want 8", next)
	}
}

func TestRepairReferenceUniquenessKeepOldest(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	first := mustCreateLegacyRepairReference(t, env, "epic", "First", project.ID, 1)
	second := mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)

	report := mustRepairReferenceUniqueness(t, env, true, "oldest")
	assertOneRenamedNode(t, report, newerReferenceNode(first, second))
	assertNoDuplicateReferences(t, env)
}

func TestRepairReferenceUniquenessKeepNewest(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	first := mustCreateLegacyRepairReference(t, env, "epic", "First", project.ID, 1)
	second := mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)

	report := mustRepairReferenceUniqueness(t, env, true, "newest")
	assertOneRenamedNode(t, report, olderReferenceNode(first, second))
	assertNoDuplicateReferences(t, env)
}

// TestRepairReferenceUniquenessKeepNewestOfThree pins the retained node for
// groups larger than two. A slice rotation would retain the second-oldest of
// three instead of the newest.
func TestRepairReferenceUniquenessKeepNewestOfThree(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	first := mustCreateLegacyRepairReference(t, env, "epic", "First", project.ID, 1)
	second := mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)
	third := mustCreateLegacyRepairReference(t, env, "issue", "Third", project.ID, 1)

	report := mustRepairReferenceUniqueness(t, env, true, "newest")
	if len(report.Renumbered) != 2 {
		t.Fatalf("renumbered = %d, want 2; report = %+v", len(report.Renumbered), report)
	}
	for _, rename := range report.Renumbered {
		if rename.NodeID == third.ID {
			t.Fatalf("the newest node %s was renumbered; it must be retained", third.ID)
		}
	}
	renamed := map[uuid.UUID]bool{report.Renumbered[0].NodeID: true, report.Renumbered[1].NodeID: true}
	if !renamed[first.ID] || !renamed[second.ID] {
		t.Fatalf("renumbered %v, want the two older nodes %s and %s", report.Renumbered, first.ID, second.ID)
	}
	assertNoDuplicateReferences(t, env)
}

func TestRepairReferenceUniquenessDryRunChangesNothing(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	mustCreateLegacyRepairReference(t, env, "epic", "First", project.ID, 1)
	mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)

	report := mustRepairReferenceUniqueness(t, env, false, "")
	if len(report.Renumbered) != 1 {
		t.Fatalf("planned renames = %d, want 1", len(report.Renumbered))
	}
	if !strings.Contains(report.Renumbered[0].To, "FAN-") {
		t.Fatalf("planned rename = %+v, want counter key FAN-", report.Renumbered[0])
	}
	assertDuplicateReferenceGroups(t, env, 1)
}

func TestRepairReferenceUniquenessRejectsUnknownKeepPolicy(t *testing.T) {
	_, err := ops.RepairReferenceUniqueness(
		t.Context(), &ops.Env{}, ops.RepairReferenceOptions{Keep: "middle"},
	)
	if err == nil {
		t.Fatal("RepairReferenceUniqueness with unknown keep policy: got nil error")
	}
	if !strings.Contains(err.Error(), "oldest") || !strings.Contains(err.Error(), "newest") {
		t.Fatalf("unknown keep error = %q, want both accepted values", err)
	}
}

func TestRepairReferenceUniquenessBackfillsReferenceKeys(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	legacy := mustCreateLegacyRepairReference(t, env, "issue", "Legacy", project.ID, 1)

	mustRepairReferenceUniqueness(t, env, true, "")
	ownerID, err := env.Stores.Nodes.LookupReference(env.Ctx, env.OrgID, "reference", "FAN-1")
	if err != nil {
		t.Fatalf("LookupReference: %v", err)
	}
	if ownerID != legacy.ID {
		t.Fatalf("LookupReference owner = %s, want %s", ownerID, legacy.ID)
	}
}

func TestRepairReferenceUniquenessSeedsBeforeFreshCreate(t *testing.T) {
	env, actor, project := repairReferenceEnv(t)
	mustCreateLegacyRepairReference(t, env, "epic", "First", project.ID, 1)
	mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)

	mustRepairReferenceUniqueness(t, env, true, "")
	mustCreate(t, env, service.CreateInput{
		ParentID: project.ID, ScopeID: project.ID, NodeTypeKey: "issue", Name: "Fresh", ActorID: actor,
	})
	assertNoDuplicateReferences(t, env)
}

func repairReferenceEnv(t *testing.T) (*TestEnv, uuid.UUID, *node.NodeView) {
	t.Helper()
	env := SetupTestEnv(t)
	registerOpsOrg(t, env)
	setGeneratedReferenceTemplates(t, env, "epic", "issue")
	actor := uuid.New()
	workspace := mustCreateScope(t, env, "workspace", "Main", env.OrgID, env.OrgID, actor)
	project := mustCreateScope(t, env, "project", "Fan", workspace.ID, workspace.ID, actor)
	return env, actor, project
}

func mustRepairReferenceUniqueness(
	t *testing.T,
	env *TestEnv,
	execute bool,
	keep string,
) ops.RepairReferenceReport {
	t.Helper()
	report, err := ops.RepairReferenceUniqueness(
		env.Ctx, env.Ops, ops.RepairReferenceOptions{Execute: execute, Keep: keep},
	)
	if err != nil {
		t.Fatalf("RepairReferenceUniqueness: %v", err)
	}
	return report
}

func assertOneRenamedNode(t *testing.T, report ops.RepairReferenceReport, want *node.NodeView) {
	t.Helper()
	if len(report.Renumbered) != 1 {
		t.Fatalf("renamed nodes = %d, want 1; report = %+v", len(report.Renumbered), report)
	}
	if report.Renumbered[0].NodeID != want.ID {
		t.Fatalf("renamed node = %s, want %s", report.Renumbered[0].NodeID, want.ID)
	}
}

func newerReferenceNode(first, second *node.NodeView) *node.NodeView {
	if first.ID.String() > second.ID.String() {
		return first
	}
	return second
}

func olderReferenceNode(first, second *node.NodeView) *node.NodeView {
	if first.ID.String() < second.ID.String() {
		return first
	}
	return second
}

func assertNoDuplicateReferences(t *testing.T, env *TestEnv) {
	t.Helper()
	assertDuplicateReferenceGroups(t, env, 0)
}

func assertDuplicateReferenceGroups(t *testing.T, env *TestEnv, want int) {
	t.Helper()
	duplicates, err := ops.FindDuplicateReferences(env.Ctx, env.Ops)
	if err != nil {
		t.Fatalf("FindDuplicateReferences: %v", err)
	}
	if len(duplicates) != want {
		t.Fatalf("duplicate groups = %d, want %d; groups = %+v", len(duplicates), want, duplicates)
	}
}
