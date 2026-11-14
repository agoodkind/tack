package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runInventoryCheck runs the production comparison script and returns its
// output and failure for assertions without failing during fixture setup.
func runInventoryCheck(t *testing.T, fixture *placementFixture, node string) (string, error) {
	t.Helper()
	script := ybInventoryCheckScript(
		filepath.Join(fixture.layout.ExportRoot, node),
		filepath.Join(fixture.artifactDir, ybDrillInventoryName(node)),
		t.TempDir(),
	)
	out, err := exec.CommandContext(t.Context(), fixture.shell, "-c", script).CombinedOutput()

	return string(out), err
}

// TestYBInventoryCheckFailsWhenTheInventoryCannotBeRead checks that missing
// inventory fails comparison instead of allowing sort's success to indicate
// that an incomplete extraction satisfies an empty expected file list.
func TestYBInventoryCheckFailsWhenTheInventoryCannotBeRead(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap = "expsnap"

			fixture := newPlacementFixture(t, shell, 2)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
			}

			fixture.archive(t, "yb1", exportSnap)
			if err := os.Remove(
				filepath.Join(
					fixture.tabletSourceDir(fixture.remaps[0], "yb1", exportSnap),
					"000123.sst",
				),
			); err != nil {
				t.Fatalf("remove one archived file from the extraction: %v", err)
			}

			if err := os.Remove(
				filepath.Join(fixture.artifactDir, ybDrillInventoryName("yb1")),
			); err != nil {
				t.Fatalf("remove the staged inventory: %v", err)
			}

			out, err := runInventoryCheck(t, fixture, "yb1")
			if err == nil {
				t.Fatalf("comparison passed without readable inventory, reporting: %q", out)
			}

			if strings.Contains(out, "missing 0") {
				t.Fatalf(
					"comparison reported zero missing files without readable inventory: %q",
					out,
				)
			}
		},
	)
}

// TestYBInventoryCheckStillReportsTheMissingFile checks that a readable
// inventory identifies the removed file and its archived size.
func TestYBInventoryCheckStillReportsTheMissingFile(t *testing.T) {
	eachPlacementShell(
		t,
		func(t *testing.T, shell string) {
			const exportSnap = "expsnap"

			fixture := newPlacementFixture(t, shell, 2)
			for _, remap := range fixture.remaps {
				fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
			}

			inventory := fixture.archive(t, "yb1", exportSnap)
			gone := filepath.Join(
				fixture.tabletSourceDir(fixture.remaps[1], "yb1", exportSnap),
				"000123.sst",
			)
			if err := os.Remove(gone); err != nil {
				t.Fatalf("remove one archived file from the extraction: %v", err)
			}

			missing := fixture.check(t, inventory)

			wantPath := ybTabletSourceDir(fixture.remaps[1], exportSnap) + "/000123.sst"
			if len(missing) != 1 || missing[0].Path != wantPath ||
				missing[0].Size != int64(len("yb1:000123.sst")) {
				t.Fatalf("missing = %+v, want only %s at its archived size", missing, wantPath)
			}
		},
	)
}

// TestParseYBInventoryCheckRefusesWhatItCannotAttribute checks that invalid,
// excessive, or inconsistent counts fail instead of reporting a partial list.
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
		t.Run(
			tc.name,
			func(t *testing.T) {
				if _, err := parseYBInventoryCheck("yb1", tc.out); err == nil {
					t.Fatalf("reading %q as an extraction check must fail", tc.out)
				}
			},
		)
	}
}

// TestParseYBInventoryCheckReadsAWholeRun checks empty and nonempty missing
// file lists, including whitespace padding from wc.
func TestParseYBInventoryCheckReadsAWholeRun(t *testing.T) {
	missing, err := parseYBInventoryCheck("yb1", "missing       0\n")
	if err != nil || len(missing) != 0 {
		t.Fatalf("whole extraction = %v, %v; want zero missing files", missing, err)
	}

	missing, err = parseYBInventoryCheck(
		"yb1",
		"missing 2\ntable-a/t.snapshots/s/CURRENT\t3\ntable-a/t.snapshots/s/x\t9\n",
	)
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
