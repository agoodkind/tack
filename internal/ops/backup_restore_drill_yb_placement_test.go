package ops

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestYBRestoreRefusesATabletMissingOneArchivedFile checks rejection of a
// partial extraction even when other files remain in the tablet directory.
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
				t.Fatalf("remove one archived file from the extraction: %v", err)
			}

			_, defects := fixture.restore(t, exportSnap, newSnap)

			want := []ybTabletDefect{
				{identity: ybTabletIdentity(damaged), carried: true, missing: 1},
			}
			if !slices.Equal(defects, want) {
				t.Fatalf("defects = %+v, want one tablet missing one file %+v", defects, want)
			}

			err := ybTabletDefectError(defects, len(fixture.remaps))
			if !strings.Contains(err.Error(), ybTabletIdentity(damaged)) {
				t.Fatalf("the failure must identify the tablet, got: %v", err)
			}

			if !strings.Contains(err.Error(), "1 archived files are missing") {
				t.Fatalf("the failure must report the missing file count, got: %v", err)
			}
		},
	)
}

// TestYBRestoreRejectsTruncatedTabletFile checks that an existing
// file with the wrong size fails replica verification.
func TestYBRestoreRejectsTruncatedTabletFile(t *testing.T) {
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
					"defects = %+v, want the tablet with a truncated file %+v",
					defects,
					want,
				)
			}
		},
	)
}

// TestYBRestoreRejectsUnarchivedTablet checks that an empty directory
// cannot substitute for tablet files absent from every archive.
func TestYBRestoreRejectsUnarchivedTablet(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap, newSnap = "expsnap", "newsnap"

			fixture := newPlacementFixture(t, shell, 3)
			for _, remap := range fixture.remaps[:2] {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
			}

			// An empty tablet directory reproduces an archive truncated after
			// directory entries but before file records.
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
				t.Fatalf("defects = %+v, want the unarchived tablet %+v", defects, want)
			}

			if err := ybTabletDefectError(defects, len(fixture.remaps)); !strings.Contains(
				err.Error(),
				"every node's archive omits it",
			) {
				t.Fatalf("the failure must report absence from every archive, got: %v", err)
			}
		},
	)
}

// TestYBRestoreCopiesOneCompleteReplicaPerTablet checks complete placement
// from one replica rather than merging different nodes' file sets. yb1 precedes
// yb2 in the manifest; yb2's extra file must not appear in the result.
func TestYBRestoreCopiesOneCompleteReplicaPerTablet(t *testing.T) {
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
				t.Fatalf("a complete export must place every tablet, refused: %+v", defects)
			}

			if audit.Expected != 4 || audit.Placed != 4 || len(audit.Missing) != 0 {
				t.Fatalf(
					"audit = expected %d, placed %d, missing %v; want 4, 4 and an empty missing list",
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
						"tablet %s counted as placed but its file is absent: %v",
						remap.new,
						err,
					)
				}

				if _, err := os.Stat(filepath.Join(placed, "EXTRA")); err == nil {
					t.Fatalf("tablet %s includes files from a second replica", remap.new)
				}
			}
		},
	)
}

// TestYBRestoreTakesTheCopyThatMatchesItsInventory checks selection of yb2's
// complete replica when the earlier yb1 extraction lacks an archived file.
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
				t.Fatalf("a complete replica on another node must pass, refused: %+v", defects)
			}

			if audit.Placed != len(fixture.remaps) {
				t.Fatalf("placed %d of %d tablets", audit.Placed, len(fixture.remaps))
			}

			for _, remap := range fixture.remaps {
				body, err := os.ReadFile(
					filepath.Join(fixture.placedTabletDir(remap, newSnap), "000123.sst"),
				)
				if err != nil {
					t.Fatalf("tablet %s counted as placed but its file is absent: %v", remap.new, err)
				}

				if !strings.HasPrefix(string(body), "yb2:") {
					t.Fatalf(
						"tablet %s copied from %q, want the replica matching its inventory",
						remap.new,
						body,
					)
				}
			}
		},
	)
}
