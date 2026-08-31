package ops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// placementShells are the shells the generated scripts are proven against. The
// drill hands them to `sh`, which is dash on some images and bash on others,
// and the two disagree on enough to matter: bash refuses a redirection whose
// unquoted expansion splits into more than one word, so a script dash accepts
// can lose every placement record under bash.
var placementShells = []string{"sh", "bash"}

// placementFixture is a throwaway export tree and destination root laid out
// the way the scratch container's is, on the local filesystem, so the
// generated scripts can be run for real. Asserting their text would prove
// nothing about the cases this file exists for: a copy whose guard is simply
// never taken, and a guard that a directory carrying nothing still passes.
type placementFixture struct {
	layout ybPlacementLayout
	remaps []ybTabletRemap
	shell  string
}

func newPlacementFixture(t *testing.T, shell string, tabletCount int) *placementFixture {
	t.Helper()
	remaps := make([]ybTabletRemap, 0, tabletCount)
	for i := range tabletCount {
		remaps = append(remaps, ybTabletRemap{
			table: fmt.Sprintf("table%02d", i),
			old:   fmt.Sprintf("old%02d", i),
			new:   fmt.Sprintf("new%02d", i),
		})
	}
	return newPlacementFixtureFor(t, shell, t.TempDir(), remaps)
}

// newPlacementFixtureFor builds a fixture rooted at an arbitrary directory and
// carrying arbitrary remaps, so a test can drive the placement with a
// configured path or a tablet id it chooses.
func newPlacementFixtureFor(t *testing.T, shell, root string, remaps []ybTabletRemap) *placementFixture {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("make fixture root %s: %v", root, err)
	}
	return &placementFixture{
		layout: ybPlacementLayout{
			ExportRoot:     filepath.Join(root, "exp"),
			RocksDBDir:     filepath.Join(root, "rocksdb"),
			ExpectedLedger: filepath.Join(root, "expected"),
			PlacedLedger:   filepath.Join(root, "placed"),
		},
		remaps: remaps,
		shell:  shell,
	}
}

// tabletSourceDir is where one node's extraction holds one tablet's files,
// which is what a complete export leaves behind.
func (f *placementFixture) tabletSourceDir(remap ybTabletRemap, node, exportSnap string) string {
	return filepath.Join(f.layout.ExportRoot, node,
		"table-"+remap.table, "tablet-"+remap.old+".snapshots", exportSnap)
}

// stageTablet writes one exported tablet's files under a node's extraction
// directory. Called with no file names it leaves the directory empty, which is
// what an archive truncated after its directory entries restores.
func (f *placementFixture) stageTablet(t *testing.T, remap ybTabletRemap, node, exportSnap string, files ...string) {
	t.Helper()
	dir := f.tabletSourceDir(remap, node, exportSnap)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("stage tablet %s: %v", remap.old, err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(node), 0o600); err != nil {
			t.Fatalf("write tablet file %s: %v", name, err)
		}
	}
}

// stageEmptyFile writes a zero-byte file into a tablet's source directory. A
// truncated archive can restore a file's name and none of its bytes, so a
// directory that holds one is still a directory that carries no data.
func (f *placementFixture) stageEmptyFile(t *testing.T, remap ybTabletRemap, node, exportSnap, name string) {
	t.Helper()
	dir := f.tabletSourceDir(remap, node, exportSnap)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("stage tablet %s: %v", remap.old, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatalf("write empty tablet file %s: %v", name, err)
	}
}

// place runs every generated placement script and then the audit script in a
// real shell, and returns what the audit read.
func (f *placementFixture) place(t *testing.T, exportSnap, newSnap string) ybPlacementAudit {
	t.Helper()
	for _, script := range ybPlacementScripts(f.remaps, f.layout, exportSnap, newSnap) {
		runShell(t, f.shell, script)
	}
	out := runShell(t, f.shell, ybPlacementAuditScript(f.layout))
	audit, err := parseYBPlacementAudit(out)
	if err != nil {
		t.Fatalf("parse placement audit %q: %v", out, err)
	}
	return audit
}

// placedTabletDir is where the placement should have put one tablet's files.
func (f *placementFixture) placedTabletDir(remap ybTabletRemap, newSnap string) string {
	return filepath.Join(f.layout.RocksDBDir,
		"table-"+remap.table, "tablet-"+remap.new+".snapshots", newSnap)
}

// runShell runs one generated script the way the drill hands it to the
// container, and fails the test if the script itself errors.
func runShell(t *testing.T, shell, script string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), shell, "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%s script failed: %v\n%s", shell, err, out)
	}
	return string(out)
}

// eachPlacementShell runs body once per shell the drill's scripts must work
// under. A shell that is not installed is reported rather than passed over in
// silence, because a skipped shell proves nothing about it.
func eachPlacementShell(t *testing.T, body func(t *testing.T, shell string)) {
	t.Helper()
	for _, shell := range placementShells {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not installed, so the scripts are unproven under it: %v", shell, err)
			}
			body(t, shell)
		})
	}
}

// TestYBPlacementFailsWhenTheExportIsMissingTablets proves the drill refuses a
// restore whose export does not carry every tablet the import created. Before
// the placement kept ledgers, the copy for a missing tablet was a directory
// test that was simply not taken, `set -e` never fired, and the drill went on
// to restore_snapshot and passed.
func TestYBPlacementFailsWhenTheExportIsMissingTablets(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 5)
		for _, remap := range fixture.remaps[:3] {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
		}

		audit := fixture.place(t, exportSnap, newSnap)

		if audit.Expected != 5 || audit.Placed != 3 {
			t.Fatalf("audit = expected %d, placed %d; want 5 attempted and 3 found",
				audit.Expected, audit.Placed)
		}
		wantMissing := []string{
			ybTabletIdentity(fixture.remaps[3]),
			ybTabletIdentity(fixture.remaps[4]),
		}
		if !slices.Equal(audit.Missing, wantMissing) {
			t.Fatalf("missing = %v, want %v", audit.Missing, wantMissing)
		}

		err := ybPlacementVerdict(audit, len(fixture.remaps))
		if err == nil {
			t.Fatal("a restore missing two tablets' files must fail the drill")
		}
		if !strings.Contains(err.Error(), "the import created 5 tablets and the export carried 3") {
			t.Fatalf("the failure must name what was expected and what was found, got: %v", err)
		}
	})
}

// TestYBPlacementPlacesEveryTabletTheExportCarries proves a complete export
// passes, that the files really land where yugabyted reads them, and that one
// replica is copied rather than several merged: yb1 sorts first, so its file
// set is the one that lands and yb2's extra file must not appear.
func TestYBPlacementPlacesEveryTabletTheExportCarries(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 4)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
			fixture.stageTablet(t, remap, "yb2", exportSnap, "CURRENT", "EXTRA")
		}

		audit := fixture.place(t, exportSnap, newSnap)

		if audit.Expected != 4 || audit.Placed != 4 || len(audit.Missing) != 0 {
			t.Fatalf("audit = expected %d, placed %d, missing %v; want 4, 4 and none",
				audit.Expected, audit.Placed, audit.Missing)
		}
		if err := ybPlacementVerdict(audit, len(fixture.remaps)); err != nil {
			t.Fatalf("a complete export must pass the drill: %v", err)
		}
		for _, remap := range fixture.remaps {
			placed := fixture.placedTabletDir(remap, newSnap)
			if _, err := os.Stat(filepath.Join(placed, "CURRENT")); err != nil {
				t.Fatalf("tablet %s was counted as placed but its files are not there: %v", remap.new, err)
			}
			if _, err := os.Stat(filepath.Join(placed, "EXTRA")); err == nil {
				t.Fatalf("tablet %s mixed a second node's replica into the copy", remap.new)
			}
		}
	})
}

// TestYBPlacementRefusesTabletsWhoseSourceCarriesNoData is the silent success
// this case exists for. An archive truncated after its directory entries
// restores the directories and none of their contents, and a directory test
// passes every one of them, so the drill placed nothing, audited clean, and let
// restore_snapshot bring the database back smaller with nothing to say it did.
// Each shape below is a directory that exists and carries no data.
func TestYBPlacementRefusesTabletsWhoseSourceCarriesNoData(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 4)
		fixture.stageTablet(t, fixture.remaps[0], "yb1", exportSnap, "CURRENT")
		// An empty directory: the archive carried the entry and no file.
		fixture.stageTablet(t, fixture.remaps[1], "yb1", exportSnap)
		// A file whose name survived truncation and whose bytes did not.
		fixture.stageEmptyFile(t, fixture.remaps[2], "yb1", exportSnap, "CURRENT")
		// Directory entries all the way down, still no file with bytes in it.
		nested := filepath.Join(fixture.tabletSourceDir(fixture.remaps[3], "yb1", exportSnap), "sub")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatalf("stage nested empty dir: %v", err)
		}

		audit := fixture.place(t, exportSnap, newSnap)

		if audit.Expected != 4 || audit.Placed != 1 {
			t.Fatalf("audit = expected %d, placed %d; want 4 attempted and only the one carrying data",
				audit.Expected, audit.Placed)
		}
		wantMissing := []string{
			ybTabletIdentity(fixture.remaps[1]),
			ybTabletIdentity(fixture.remaps[2]),
			ybTabletIdentity(fixture.remaps[3]),
		}
		if !slices.Equal(audit.Missing, wantMissing) {
			t.Fatalf("missing = %v, want the three tablets carrying no data %v", audit.Missing, wantMissing)
		}
		if err := ybPlacementVerdict(audit, len(fixture.remaps)); err == nil {
			t.Fatal("a restore whose export carried only directory entries must fail the drill")
		}
	})
}

// TestYBPlacementTakesTheReplicaThatCarriesData proves the loop breaks on the
// first source carrying data rather than the first that merely exists, so a
// node whose extraction came back empty does not hide a good replica on
// another node. yb1 sorts first and carries nothing; yb2 is the copy that must
// land.
func TestYBPlacementTakesTheReplicaThatCarriesData(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 2)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap)
			fixture.stageTablet(t, remap, "yb2", exportSnap, "CURRENT")
		}

		audit := fixture.place(t, exportSnap, newSnap)

		if audit.Expected != 2 || audit.Placed != 2 {
			t.Fatalf("audit = expected %d, placed %d; want both placed from the node that carried them",
				audit.Expected, audit.Placed)
		}
		for _, remap := range fixture.remaps {
			body, err := os.ReadFile(filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT"))
			if err != nil {
				t.Fatalf("tablet %s was counted as placed but its files are not there: %v", remap.new, err)
			}
			if string(body) != "yb2" {
				t.Fatalf("tablet %s was copied from %q, want the replica carrying data", remap.new, body)
			}
		}
	})
}

// TestYBPlacementHandlesConfiguredPathsWithASpace proves a configured directory
// carrying a space places every tablet. Unquoted, the export root split the
// source glob into two words so no tablet was ever found, and the ledger paths
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

		audit := fixture.place(t, exportSnap, newSnap)

		if audit.Expected != 2 || audit.Placed != 2 || len(audit.Missing) != 0 {
			t.Fatalf("audit = expected %d, placed %d, missing %v; want both placed from a path with a space",
				audit.Expected, audit.Placed, audit.Missing)
		}
		if err := ybPlacementVerdict(audit, len(remaps)); err != nil {
			t.Fatalf("a path with a space must not fail the drill: %v", err)
		}
		for _, remap := range remaps {
			if _, err := os.Stat(filepath.Join(fixture.placedTabletDir(remap, newSnap), "CURRENT")); err != nil {
				t.Fatalf("tablet %s did not land under a path with a space: %v", remap.new, err)
			}
		}
	})
}

// TestYBPlacementTreatsTabletIdsAsData proves the ids the placement builds its
// paths from are data. They are fields of `yb-admin import_snapshot` output and
// are matched against no pattern, so metadata reaching them is a path to
// execution: Go's %q wrapped them in double quotes, which a shell still expands,
// leaving $(...) and backticks live inside a container holding the cluster's
// data. The payloads below must land as directory names, and their canaries
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

		audit := fixture.place(t, exportSnap, newSnap)

		for _, canary := range []string{substitution, backtick} {
			if _, err := os.Stat(canary); err == nil {
				t.Fatalf("a tablet id ran as shell: it created %s", canary)
			}
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
