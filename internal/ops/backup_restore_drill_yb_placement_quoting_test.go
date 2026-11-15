package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestYBPlacementHandlesConfiguredPathsWithASpace checks that placement copies
// every tablet and records each copy when configured directories contain spaces.
func TestYBPlacementHandlesConfiguredPathsWithASpace(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
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
				t.Fatalf(
					"audit = expected %d, placed %d, missing %v; want both placed from a path with a space",
					audit.Expected,
					audit.Placed,
					audit.Missing,
				)
			}

			for _, remap := range remaps {
				if _, err := os.Stat(
					filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT"),
				); err != nil {
					t.Fatalf("tablet %s is missing from the path with a space: %v", remap.new, err)
				}
			}
		},
	)
}

// TestYBPlacementTreatsTabletIdsAsData bypasses import-output validation with
// shell command substitutions in tablet IDs. Placement must preserve the literal
// directory names without executing either substitution or creating its file.
func TestYBPlacementTreatsTabletIdsAsData(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"
			root := t.TempDir()
			substitution := filepath.Join(root, "substituted")
			backtick := filepath.Join(root, "backticked")
			remaps := []ybTabletRemap{
				{table: "table00", old: "old00", new: "new$(printf injected >" + substitution + ")00"},
				{table: "table01", old: "old01", new: "new`printf injected >" + backtick + "`01"},
			}

			fixture := newPlacementFixtureFor(t, shell, root, remaps)
			for _, remap := range remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
			}

			fixture.archive(t, "yb1", exportSnap)

			audit, defects := fixture.restore(t, exportSnap, newSnap)

			for _, canary := range []string{substitution, backtick} {
				if _, err := os.Stat(canary); err == nil {
					t.Fatalf("a tablet ID executed as shell and created %s", canary)
				}
			}

			if len(defects) != 0 {
				t.Fatalf("placement must accept literal tablet IDs: %+v", defects)
			}

			if audit.Expected != 2 || audit.Placed != 2 {
				t.Fatalf(
					"audit = expected %d, placed %d; want both literal tablet IDs",
					audit.Expected,
					audit.Placed,
				)
			}

			for _, remap := range remaps {
				if _, err := os.Stat(
					filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT"),
				); err != nil {
					t.Fatalf("tablet %q is missing from its literal directory: %v", remap.new, err)
				}
			}
		},
	)
}
