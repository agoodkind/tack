package ops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestYBPlacementHandlesConfiguredPathsWithASpace checks that placement
// copies every tablet when configured directories contain spaces. Previously,
// an unquoted export root split the source path into words and prevented tablet
// discovery. Unquoted ledger paths assigned only their first word; bash treated
// the resulting redirect as ambiguous and lost placement records.
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

// TestYBPlacementTreatsTabletIdsAsData checks that placement treats IDs as
// directory names. The IDs come from `yb-admin import_snapshot` output,
// and placement does not validate their format. Previously, Go's %q used
// double quotes that still allowed shell expansion of $(...) and backticks
// inside a container with cluster data. The parser now rejects IDs outside
// the engine's format before placement. These test payloads bypass the parser
// to check placement quoting independently: literal directories must exist,
// and command-substitution canaries must not exist.
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
