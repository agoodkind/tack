package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// runInventoryCheck invokes the generated node comparison and returns its
// output and exit error. Tests can check expected program failures without
// the fixture failing first.
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

// TestYBInventoryCheckFailsWhenTheInventoryCannotBeRead checks an earlier
// false success. A pipeline exposed only sort's successful exit after the
// inventory read failed. The empty comparison baseline could report zero
// missing files without checking the tablet against its archived contents.
//
// This fixture removes an archived file from the extraction and removes the
// inventory. The comparison must fail on the read instead of reporting zero
// missing files.
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
				t.Fatalf("take one archived file out of the extraction: %v", err)
			}

			if err := os.Remove(
				filepath.Join(fixture.artifactDir, ybDrillInventoryName("yb1")),
			); err != nil {
				t.Fatalf("take the staged inventory away: %v", err)
			}

			out, err := runInventoryCheck(t, fixture, "yb1")
			if err == nil {
				t.Fatalf("a check whose inventory could not be read passed, reporting: %q", out)
			}

			if strings.Contains(out, "missing 0") {
				t.Fatalf(
					"a check whose inventory could not be read reported nothing missing: %q",
					out,
				)
			}
		},
	)
}

// TestYBInventoryCheckStillReportsTheMissingFile checks the successful
// comparison after the pipeline correction. The result must identify the one
// removed file and the size that the archive recorded.
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
				t.Fatalf("take one archived file out of the extraction: %v", err)
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

// TestParseYBInventoryCheckRefusesWhatItCannotAttribute requires the parser
// to reject incomplete reports. Every missing file requires tablet attribution.
// An unreportable count or a count that disagrees with the file lines must fail
// instead of leaving affected tablets incorrectly classified as complete.
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

// TestParseYBInventoryCheckReadsAWholeRun checks both complete and incomplete
// extractions. The parser accepts wc padding in the count and returns each
// reported missing file.
func TestParseYBInventoryCheckReadsAWholeRun(t *testing.T) {
	missing, err := parseYBInventoryCheck("yb1", "missing       0\n")
	if err != nil || len(missing) != 0 {
		t.Fatalf("whole extraction = %v, %v; want none missing", missing, err)
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
