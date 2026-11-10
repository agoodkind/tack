package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestYBPlacementHandlesConfiguredPathsWithASpace proves a configured directory
// carrying a space places every tablet. Unquoted, the export root split the
// source path into two words so no tablet was ever found, and the ledger paths
// bound only their first word, which under bash is an ambiguous redirect that
// loses every record the placement writes.
func TestYBPlacementHandlesConfiguredPathsWithASpace(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		remaps := []ybTabletRemap{
			{table: "table00", old: "old00", new: "new00"},
			{table: "table01", old: "old01", new: "new01"},
		}
		root := filepath.Join(t.TempDir(), "yb data", "restore drill")
		fixture := newPlacementFixtureFor(t, shell, root, remaps)
		for _, remap := range remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
		}
		fixture.archive(t, "yb1", exportSnap)

		audit, defects := fixture.restore(t, exportSnap, newSnap)

		if len(defects) != 0 {
			t.Fatalf("a path with a space must not refuse a tablet: %+v", defects)
		}
		if audit.Expected != 2 || audit.Placed != 2 || len(audit.Missing) != 0 {
			t.Fatalf("audit = expected %d, placed %d, missing %v; want both placed from a path with a space",
				audit.Expected, audit.Placed, audit.Missing)
		}
		for _, remap := range remaps {
			if _, err := os.Stat(filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT")); err != nil {
				t.Fatalf("tablet %s did not land under a path with a space: %v", remap.new, err)
			}
		}
	})
}

// TestYBPlacementTreatsTabletIdsAsData proves the ids the placement builds its
// paths from are data. They are fields of `yb-admin import_snapshot` output,
// and the placement itself matches them against no pattern, so metadata
// reaching them is a path to execution: Go's %q wrapped them in double quotes,
// which a shell still expands, leaving $(...) and backticks live inside a
// container holding the cluster's data. The parser now refuses ids outside the
// engine's form before they get here; the payloads below bypass it to prove the
// placement holds on its own, must land as directory names, and their canaries
// must not exist.
func TestYBPlacementTreatsTabletIdsAsData(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		root := t.TempDir()
		substitution := filepath.Join(root, "substituted")
		backtick := filepath.Join(root, "backticked")
		remaps := []ybTabletRemap{
			{table: "table00", old: "old00", new: "new$(touch " + substitution + ")00"},
			{table: "table01", old: "old01", new: "new`touch " + backtick + "`01"},
		}
		fixture := newPlacementFixtureFor(t, shell, root, remaps)
		for _, remap := range remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
		}
		fixture.archive(t, "yb1", exportSnap)

		audit, defects := fixture.restore(t, exportSnap, newSnap)

		for _, canary := range []string{substitution, backtick} {
			if _, err := os.Stat(canary); err == nil {
				t.Fatalf("a tablet id ran as shell: it created %s", canary)
			}
		}
		if len(defects) != 0 {
			t.Fatalf("ids handled as data must still place: %+v", defects)
		}
		if audit.Expected != 2 || audit.Placed != 2 {
			t.Fatalf("audit = expected %d, placed %d; want both ids handled as data",
				audit.Expected, audit.Placed)
		}
		for _, remap := range remaps {
			if _, err := os.Stat(filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT")); err != nil {
				t.Fatalf("tablet %q did not land under its literal id: %v", remap.new, err)
			}
		}
	})
}
