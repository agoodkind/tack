package ops

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestYBRestoreRefusesATabletMissingOneArchivedFile checks a partial-copy
// regression. Earlier directory checks accepted a nonempty extraction despite
// a missing file and reported a complete restore. This test removes one
// archived file while retaining the others, then checks that the inventory
// comparison refuses the tablet and reports its identity and missing-file
// count.
func TestYBRestoreRefusesATabletMissingOneArchivedFile(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 4)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(
					t,
					remap,
					"yb1",
					exportSnap,
					"CURRENT",
					"000123.sst",
					"MANIFEST-000004",
				)
			}

			damaged := fixture.remaps[2]
			fixture.archive(t, "yb1", exportSnap)
			if err := os.Remove(
				filepath.Join(fixture.tabletSourceDir(damaged, "yb1", exportSnap), "000123.sst"),
			); err != nil {
				t.Fatalf("take one archived file out of the extraction: %v", err)
			}

			_, defects := fixture.restore(t, exportSnap, newSnap)

			want := []ybTabletDefect{
				{identity: ybTabletIdentity(damaged), carried: true, missing: 1},
			}
			if !slices.Equal(defects, want) {
				t.Fatalf("defects = %+v, want the one tablet short of one file %+v", defects, want)
			}

			err := ybTabletDefectError(defects, len(fixture.remaps))
			if !strings.Contains(err.Error(), ybTabletIdentity(damaged)) {
				t.Fatalf("the failure must name the tablet, got: %v", err)
			}

			if !strings.Contains(err.Error(), "1 archived files are missing") {
				t.Fatalf("the failure must say how many files are missing, got: %v", err)
			}
		},
	)
}

// TestYBRestoreRefusesATabletWhoseFileLostItsBytes checks recorded file sizes.
// A presence check alone accepts a truncated file. This test shortens one
// archived file and requires a tablet defect.
func TestYBRestoreRefusesATabletWhoseFileLostItsBytes(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 3)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
			}

			fixture.archive(t, "yb1", exportSnap)
			truncated := fixture.remaps[1]
			short := filepath.Join(
				fixture.tabletSourceDir(truncated, "yb1", exportSnap),
				"000123.sst",
			)
			if err := os.WriteFile(short, []byte("x"), 0o600); err != nil {
				t.Fatalf("shorten one archived file: %v", err)
			}

			_, defects := fixture.restore(t, exportSnap, newSnap)

			want := []ybTabletDefect{
				{identity: ybTabletIdentity(truncated), carried: true, missing: 1},
			}
			if !slices.Equal(defects, want) {
				t.Fatalf(
					"defects = %+v, want the tablet whose file lost its bytes %+v",
					defects,
					want,
				)
			}
		},
	)
}

// TestYBRestoreRefusesATabletNoArchiveCarried checks refusal of an imported
// tablet that lacks archived files on every node. An existing directory does
// not qualify as a copy; the test checks the tablet's identity in the defect.
func TestYBRestoreRefusesATabletNoArchiveCarried(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 3)
			for _, remap := range fixture.remaps[:2] {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
			}

			// This empty directory models an extraction after archive truncation.
			// The inventory does not record any files for it.
			if err := os.MkdirAll(
				fixture.tabletSourceDir(fixture.remaps[2], "yb1", exportSnap),
				0o755,
			); err != nil {
				t.Fatalf("stage an empty tablet directory: %v", err)
			}

			fixture.archive(t, "yb1", exportSnap)

			_, defects := fixture.restore(t, exportSnap, newSnap)

			want := []ybTabletDefect{
				{identity: ybTabletIdentity(fixture.remaps[2]), carried: false, missing: 0},
			}
			if !slices.Equal(defects, want) {
				t.Fatalf("defects = %+v, want the uncarried tablet %+v", defects, want)
			}

			if err := ybTabletDefectError(defects, len(fixture.remaps)); !strings.Contains(
				err.Error(),
				"no node's archive carried it",
			) {
				t.Fatalf("the failure must say the export never carried it, got: %v", err)
			}
		},
	)
}

// TestYBRestorePlacesEveryTabletTheArchiveCarries checks a complete export
// against the placement verdict and the snapshot directories that yugabyted
// uses. It checks the expected files and excludes yb2's extra file. The first
// complete copy belongs to yb1; placement must not merge copies from both
// nodes.
func TestYBRestorePlacesEveryTabletTheArchiveCarries(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 4)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
				fixture.stageTablet(t, remap, "yb2", exportSnap, "CURRENT", "000123.sst", "EXTRA")
			}

			fixture.archive(t, "yb1", exportSnap)
			fixture.archive(t, "yb2", exportSnap)

			audit, defects := fixture.restore(t, exportSnap, newSnap)

			if len(defects) != 0 {
				t.Fatalf("a whole export must place every tablet, refused: %+v", defects)
			}

			if audit.Expected != 4 || audit.Placed != 4 || len(audit.Missing) != 0 {
				t.Fatalf(
					"audit = expected %d, placed %d, missing %v; want 4, 4 and none",
					audit.Expected,
					audit.Placed,
					audit.Missing,
				)
			}

			if err := ybPlacementVerdict(audit, len(fixture.remaps)); err != nil {
				t.Fatalf("a complete export must pass the drill: %v", err)
			}

			for _, remap := range fixture.remaps {
				placed := fixture.placedTabletDir(remap, newSnap)
				if _, err := os.Stat(filepath.Join(placed, "000123.sst")); err != nil {
					t.Fatalf(
						"tablet %s was counted as placed but its files are not there: %v",
						remap.new,
						err,
					)
				}

				if _, err := os.Stat(filepath.Join(placed, "EXTRA")); err == nil {
					t.Fatalf("tablet %s mixed a second node's copy into the copy", remap.new)
				}
			}
		},
	)
}

// TestYBRestoreTakesTheCopyThatMatchesItsInventory checks selection of a
// complete copy after an incomplete one. The test removes a file from yb1, then
// checks that placement uses yb2's files for every tablet.
func TestYBRestoreTakesTheCopyThatMatchesItsInventory(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 2)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
				fixture.stageTablet(t, remap, "yb2", exportSnap, "CURRENT", "000123.sst")
			}

			fixture.archive(t, "yb1", exportSnap)
			fixture.archive(t, "yb2", exportSnap)

			for _, remap := range fixture.remaps {
				if err := os.Remove(
					filepath.Join(fixture.tabletSourceDir(remap, "yb1", exportSnap), "000123.sst"),
				); err != nil {
					t.Fatalf("damage yb1's extraction: %v", err)
				}
			}

			audit, defects := fixture.restore(t, exportSnap, newSnap)

			if len(defects) != 0 {
				t.Fatalf("a whole copy on another node must be taken, refused: %+v", defects)
			}

			if audit.Placed != len(fixture.remaps) {
				t.Fatalf("placed %d of %d tablets", audit.Placed, len(fixture.remaps))
			}

			for _, remap := range fixture.remaps {
				body, err := os.ReadFile(
					filepath.Join(fixture.placedTabletDir(remap, newSnap), "000123.sst"),
				)
				if err != nil {
					t.Fatalf("tablet %s was placed but its files are not there: %v", remap.new, err)
				}

				if !strings.HasPrefix(string(body), "yb2:") {
					t.Fatalf(
						"tablet %s was copied from %q, want the copy that matched its inventory",
						remap.new,
						body,
					)
				}
			}
		},
	)
}
