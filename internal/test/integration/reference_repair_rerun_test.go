package integration

import (
	"testing"

	"goodkind.io/tack/internal/ops"
)

// TestRepairReferenceUniquenessReportsOnlyChanges pins what a run reports,
// because the command records one ledger event per reported item. The first
// run raises the scope counter, renames the duplicate, and claims the kept
// node's key; the renamed node's new key is claimed by the rename itself, so
// it is not a second key write. A rerun finds the counter already past the
// renamed node, every key held, and nothing to rename, so it reports zeros
// and the ledger gains nothing. On the testbed the rerun once reported 11
// counter seeds and 1621 key writes and recorded 7 counter rows for seeds
// that changed nothing (TACK-448).
func TestRepairReferenceUniquenessReportsOnlyChanges(t *testing.T) {
	env, _, project := repairReferenceEnv(t)
	mustCreateLegacyRepairReference(t, env, "issue", "First", project.ID, 1)
	mustCreateLegacyRepairReference(t, env, "issue", "Second", project.ID, 1)

	plan := mustRepairReferenceUniqueness(t, env, false, "")
	assertRepairCounts(t, "plan before the repair", plan, 1, 1, 0)

	first := mustRepairReferenceUniqueness(t, env, true, "")
	assertRepairCounts(t, "first execute", first, 1, 1, 1)

	replan := mustRepairReferenceUniqueness(t, env, false, "")
	assertRepairCounts(t, "plan after the repair", replan, 0, 0, 0)

	second := mustRepairReferenceUniqueness(t, env, true, "")
	assertRepairCounts(t, "second execute", second, 0, 0, 0)
	assertNoDuplicateReferences(t, env)
}

// TestRaiseSequenceByKeyReportsWhetherItChangedTheCounter pins the store
// primitive the repair records against: raising reports true only when the
// counter moved, never lowers it, and a peek reads it without moving it.
func TestRaiseSequenceByKeyReportsWhetherItChangedTheCounter(t *testing.T) {
	env := SetupTestEnv(t)
	assertRaise(t, env, 40, true)
	assertRaise(t, env, 40, false)
	assertRaise(t, env, 10, false)
	assertPeek(t, env, 40)

	next, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("AllocateSequenceByKey: %v", err)
	}
	if next != 41 {
		t.Fatalf("allocated %d, want 41 after raising to 40", next)
	}
	assertPeek(t, env, 41)
	assertRaise(t, env, 41, false)
	assertRaise(t, env, 50, true)
	assertPeek(t, env, 50)
}

func assertRepairCounts(
	t *testing.T,
	step string,
	report ops.RepairReferenceReport,
	renames, counters, keys int,
) {
	t.Helper()
	if len(report.Renumbered) != renames || len(report.Counters) != counters || len(report.Keys) != keys {
		t.Fatalf("%s reported %d renames, %d counters, %d keys; want %d, %d, %d",
			step, len(report.Renumbered), len(report.Counters), len(report.Keys), renames, counters, keys)
	}
}

func assertRaise(t *testing.T, env *TestEnv, value int64, wantRaised bool) {
	t.Helper()
	raised, err := env.Stores.Nodes.RaiseSequenceByKey(env.Ctx, env.OrgID, "FAN-", value)
	if err != nil {
		t.Fatalf("RaiseSequenceByKey to %d: %v", value, err)
	}
	if raised != wantRaised {
		t.Fatalf("RaiseSequenceByKey to %d reported raised=%t, want %t", value, raised, wantRaised)
	}
}

func assertPeek(t *testing.T, env *TestEnv, want int64) {
	t.Helper()
	got, err := env.Stores.Nodes.PeekSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("PeekSequenceByKey: %v", err)
	}
	if got != want {
		t.Fatalf("PeekSequenceByKey = %d, want %d", got, want)
	}
}
