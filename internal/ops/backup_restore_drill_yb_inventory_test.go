package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runInventoryCheck runs the generated comparison for one node the way the
// drill does and returns its output and exit error, so a test can assert on a
// check that must fail rather than have the fixture fail it first.
func runInventoryCheck(t *testing.T, fixture *placementFixture, node string) (string, error) {
	t.Helper()
	script := ybInventoryCheckScript(
		filepath.Join(fixture.layout.ExportRoot, node),
		filepath.Join(fixture.artifactDir, ybDrillInventoryName(node)),
		t.TempDir())
	out, err := exec.CommandContext(t.Context(), fixture.shell, "-c", script).CombinedOutput()
	return string(out), err
}

// TestYBInventoryCheckFailsWhenTheInventoryCannotBeRead is the silent success
// this file exists for. The inventory is what the extraction is judged against,
// and the check read it through a pipeline, whose exit status is the sort's:
// an inventory that could not be read sorted to nothing, the check wanted no
// files, found none missing, and passed a tablet it had verified against
// nothing. The extraction below is short of a file its archive carried, and
// its inventory is gone; the check must fail on the read, not report zero
// missing.
func TestYBInventoryCheckFailsWhenTheInventoryCannotBeRead(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap = "expsnap"
		fixture := newPlacementFixture(t, shell, 2)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
		}
		fixture.archive(t, "yb1", exportSnap)
		if err := os.Remove(filepath.Join(fixture.tabletSourceDir(fixture.remaps[0], "yb1", exportSnap), "000123.sst")); err != nil {
			t.Fatalf("take one archived file out of the extraction: %v", err)
		}
		if err := os.Remove(filepath.Join(fixture.artifactDir, ybDrillInventoryName("yb1"))); err != nil {
			t.Fatalf("take the staged inventory away: %v", err)
		}

		out, err := runInventoryCheck(t, fixture, "yb1")

		if err == nil {
			t.Fatalf("a check whose inventory could not be read passed, reporting: %q", out)
		}
		if strings.Contains(out, "missing 0") {
			t.Fatalf("a check whose inventory could not be read reported nothing missing: %q", out)
		}
	})
}

// TestYBInventoryCheckStillReportsTheMissingFile proves the restructured
// check gives the same answer as before when every step works: the one file
// taken out of the extraction is named, with the size the archive recorded.
func TestYBInventoryCheckStillReportsTheMissingFile(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap = "expsnap"
		fixture := newPlacementFixture(t, shell, 2)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
		}
		inventory := fixture.archive(t, "yb1", exportSnap)
		gone := filepath.Join(fixture.tabletSourceDir(fixture.remaps[1], "yb1", exportSnap), "000123.sst")
		if err := os.Remove(gone); err != nil {
			t.Fatalf("take one archived file out of the extraction: %v", err)
		}

		missing := fixture.check(t, inventory)

		wantPath := ybTabletSourceDir(fixture.remaps[1], exportSnap) + "/000123.sst"
		if len(missing) != 1 || missing[0].Path != wantPath || missing[0].Size != int64(len("yb1:000123.sst")) {
			t.Fatalf("missing = %+v, want only %s at its archived size", missing, wantPath)
		}
	})
}

// TestParseYBInventoryCheckRefusesWhatItCannotAttribute proves the extraction
// check never reports a subset. Each missing file is attributed to the tablet
// it belongs to, so a count the report cannot cover, or one that disagrees with
// the lines, has to fail rather than leave tablets it could not account for
// looking whole.
func TestParseYBInventoryCheckRefusesWhatItCannotAttribute(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "no count", out: "table-a/x\t3\n"},
		{name: "unreadable count", out: "missing lots\n"},
		{name: "count above the report limit", out: "missing 4097\n"},
		{name: "count disagrees with the lines", out: "missing 2\ntable-a/x\t3\n"},
		{name: "an unreadable line", out: "missing 1\ntable-a/x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseYBInventoryCheck("yb1", tc.out); err == nil {
				t.Fatalf("reading %q as an extraction check must fail", tc.out)
			}
		})
	}
}

// TestParseYBInventoryCheckReadsAWholeRun proves the two answers the check
// gives: nothing missing, and the files that are, padding from wc included.
func TestParseYBInventoryCheckReadsAWholeRun(t *testing.T) {
	missing, err := parseYBInventoryCheck("yb1", "missing       0\n")
	if err != nil || len(missing) != 0 {
		t.Fatalf("whole extraction = %v, %v; want none missing", missing, err)
	}
	missing, err = parseYBInventoryCheck("yb1", "missing 2\ntable-a/t.snapshots/s/CURRENT\t3\ntable-a/t.snapshots/s/x\t9\n")
	if err != nil {
		t.Fatalf("parseYBInventoryCheck: %v", err)
	}
	want := []ybArchivedFile{
		{Path: "table-a/t.snapshots/s/CURRENT", Size: 3},
		{Path: "table-a/t.snapshots/s/x", Size: 9},
	}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing:\n got=%+v\nwant=%+v", missing, want)
	}
}
