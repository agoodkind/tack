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

// placementFixture is a throwaway data guest and scratch container laid out the
// way the real ones are, on the local filesystem, so the whole chain can be run
// for real: each node's staged tablet tree is archived by the export's own tar
// command, the inventory is read back out of that archive, the archive is
// unpacked by the drill's own extraction program, the extraction is compared
// against the inventory by the generated check, and the placement and audit
// scripts run in a real shell. Asserting the scripts' text would prove nothing
// about the cases this file exists for, which are all about what a directory
// turns out to hold once the bytes have made the round trip.
type placementFixture struct {
	layout ybPlacementLayout
	remaps []ybTabletRemap
	shell  string
	// stageRoot stands in for a data guest's rocksdb root, holding one
	// subdirectory per node of the tablets that node would archive. It is a
	// separate tree from the extraction, so what the drill checks is what came
	// back out of the archive rather than what went in.
	stageRoot string
	// artifactDir stands in for the drill's staging directory, which the
	// scratch container sees read-only at /artifacts.
	artifactDir string
	// runID is the export run the fixture's inventories declare.
	runID string
	// inventories are what each archived node's tar was recorded to carry, in
	// the order the nodes were archived, which is the order copies are
	// preferred in.
	inventories []ybArchiveInventory
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
		remaps:      remaps,
		shell:       shell,
		stageRoot:   filepath.Join(root, "guests"),
		artifactDir: filepath.Join(root, "artifacts"),
		runID:       "20260830T010203Z",
		inventories: nil,
	}
}

// tabletSourceDir is where one node's extraction holds one tablet's files,
// which is what a whole archive leaves behind and what the drill copies from.
func (f *placementFixture) tabletSourceDir(remap ybTabletRemap, node, exportSnap string) string {
	return filepath.Join(f.layout.ExportRoot, node, ybTabletSourceDir(remap, exportSnap))
}

// stageTablet writes one tablet's files under a node's own rocksdb root, which
// is what that node's archive command will tar. Each file's body names the node
// and the file, so a test can tell which node's copy landed and two nodes'
// copies of one file differ in size.
func (f *placementFixture) stageTablet(t *testing.T, remap ybTabletRemap, node, exportSnap string, files ...string) {
	t.Helper()
	dir := filepath.Join(f.stageRoot, node, ybTabletSourceDir(remap, exportSnap))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("stage tablet %s: %v", remap.old, err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(node+":"+name), 0o600); err != nil {
			t.Fatalf("write tablet file %s: %v", name, err)
		}
	}
}

// archive tars one node's staged tree the way the export's node archive command
// does, records the inventory read back out of that archive, stages both the
// way the drill stages them, and unpacks the archive with the drill's own
// extraction program. Anything a test does to the extraction afterwards is
// damage the record still remembers, which is the situation the drill exists to
// catch.
func (f *placementFixture) archive(t *testing.T, node, exportSnap string) ybArchiveInventory {
	t.Helper()
	if err := os.MkdirAll(f.artifactDir, 0o755); err != nil {
		t.Fatalf("make the fixture staging dir: %v", err)
	}
	tarPath := filepath.Join(f.artifactDir, ybDrillArchiveName(node))
	// COPYFILE_DISABLE stops the bsdtar on a macOS developer machine adding an
	// AppleDouble "._name" member per entry, which the Linux container the
	// export actually runs in never produces. Without it the fixture's archive
	// would record files that exist in no extraction anywhere.
	runShell(t, f.shell, "cd "+shellQuote(filepath.Join(f.stageRoot, node))+
		" && find . -type d -path "+shellQuote("*.snapshots/"+exportSnap)+
		" -print0 | COPYFILE_DISABLE=1 tar czf "+shellQuote(tarPath)+" --null --files-from -")
	inventory, err := readYBArchiveInventory(t.Context(), f.runID, node, tarPath)
	if err != nil {
		t.Fatalf("inventory node %s: %v", node, err)
	}
	if err := os.WriteFile(filepath.Join(f.artifactDir, ybDrillInventoryName(node)),
		inventory.render(), 0o600); err != nil {
		t.Fatalf("stage the inventory for node %s: %v", node, err)
	}
	f.extract(t, node)
	f.inventories = append(f.inventories, inventory)
	return inventory
}

// extract runs the drill's extraction program over one node's staged archive.
// The fixture's staging directory stands in for the container's read-only
// /artifacts mount; nothing else about the program changes.
func (f *placementFixture) extract(t *testing.T, node string) {
	t.Helper()
	runShellArgs(t, f.shell, ybTabletExtractScript(f.layout.ExportRoot, f.artifactDir), node)
}

// check runs the generated comparison program for one node against the
// inventory staged beside its archive, and returns what the extraction owes it.
func (f *placementFixture) check(t *testing.T, inventory ybArchiveInventory) []ybArchivedFile {
	t.Helper()
	out := runShell(t, f.shell, ybInventoryCheckScript(
		filepath.Join(f.layout.ExportRoot, inventory.Node),
		filepath.Join(f.artifactDir, ybDrillInventoryName(inventory.Node)),
		t.TempDir()))
	missing, err := parseYBInventoryCheck(inventory.Node, out)
	if err != nil {
		t.Fatalf("parse the extraction check for node %s from %q: %v", inventory.Node, out, err)
	}
	return missing
}

// restore runs the rest of the chain over what the fixture archived: check each
// extraction against its own node's record, choose a copy per tablet, and place
// them. The audit is meaningful only when no tablet was refused, because
// production stops before the placement when one is.
func (f *placementFixture) restore(t *testing.T, exportSnap, newSnap string) (ybPlacementAudit, []ybTabletDefect) {
	t.Helper()
	extractions := make([]ybNodeExtraction, 0, len(f.inventories))
	for _, inventory := range f.inventories {
		extractions = append(extractions, newYBNodeExtraction(inventory, f.check(t, inventory)))
	}
	placements, defects := chooseYBTabletReplicas(f.remaps, f.layout.ExportRoot, exportSnap, extractions)
	if len(defects) > 0 {
		return ybPlacementAudit{Expected: 0, Placed: 0, Missing: nil}, defects
	}
	for _, script := range ybPlacementScripts(placements, f.layout, newSnap) {
		runShell(t, f.shell, script)
	}
	out := runShell(t, f.shell, ybPlacementAuditScript(f.layout))
	audit, err := parseYBPlacementAudit(out)
	if err != nil {
		t.Fatalf("parse placement audit %q: %v", out, err)
	}
	return audit, nil
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
	return runShellArgs(t, shell, script)
}

// runShellArgs runs a generated script that reads positional parameters, passing
// them the way the drill does: the program, the name the shell reports as $0,
// then the values.
func runShellArgs(t *testing.T, shell, script string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", script, shell}, args...)
	out, err := exec.CommandContext(t.Context(), shell, argv...).CombinedOutput()
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
			if _, err := exec.LookPath("tar"); err != nil {
				t.Skipf("tar is not installed, so no fixture archive can be built: %v", err)
			}
			body(t, shell)
		})
	}
}

// TestYBRestoreRefusesATabletMissingOneArchivedFile is the silent success this
// design exists for. A tablet whose extraction lost one of its files still
// holds files, and every question the drill used to ask of the directory itself
// answered yes, so the partial copy was placed and the restore reported a whole
// database. Judged against what the archive recorded, one absent file is enough
// to refuse the tablet by name.
func TestYBRestoreRefusesATabletMissingOneArchivedFile(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 4)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst", "MANIFEST-000004")
		}
		damaged := fixture.remaps[2]
		fixture.archive(t, "yb1", exportSnap)
		if err := os.Remove(filepath.Join(fixture.tabletSourceDir(damaged, "yb1", exportSnap), "000123.sst")); err != nil {
			t.Fatalf("take one archived file out of the extraction: %v", err)
		}

		_, defects := fixture.restore(t, exportSnap, newSnap)

		want := []ybTabletDefect{{identity: ybTabletIdentity(damaged), carried: true, missing: 1}}
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
	})
}

// TestYBRestoreRefusesATabletWhoseFileLostItsBytes proves the check is about
// bytes and not names. A file that arrives at the wrong size is present, so
// every test that asks whether a file is there passes it, and the tablet it
// belongs to is silently short of data.
func TestYBRestoreRefusesATabletWhoseFileLostItsBytes(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 3)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
		}
		fixture.archive(t, "yb1", exportSnap)
		truncated := fixture.remaps[1]
		short := filepath.Join(fixture.tabletSourceDir(truncated, "yb1", exportSnap), "000123.sst")
		if err := os.WriteFile(short, []byte("x"), 0o600); err != nil {
			t.Fatalf("shorten one archived file: %v", err)
		}

		_, defects := fixture.restore(t, exportSnap, newSnap)

		want := []ybTabletDefect{{identity: ybTabletIdentity(truncated), carried: true, missing: 1}}
		if !slices.Equal(defects, want) {
			t.Fatalf("defects = %+v, want the tablet whose file lost its bytes %+v", defects, want)
		}
	})
}

// TestYBRestoreRefusesATabletNoArchiveCarried proves a tablet the import
// created that no node's archive holds is refused and named, rather than
// counted as placed because its directory happened to exist.
func TestYBRestoreRefusesATabletNoArchiveCarried(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 3)
		for _, remap := range fixture.remaps[:2] {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT")
		}
		// The directory an archive truncated after its entries restores: it
		// exists, it holds nothing, and no file of it was ever recorded.
		if err := os.MkdirAll(fixture.tabletSourceDir(fixture.remaps[2], "yb1", exportSnap), 0o755); err != nil {
			t.Fatalf("stage an empty tablet directory: %v", err)
		}
		fixture.archive(t, "yb1", exportSnap)

		_, defects := fixture.restore(t, exportSnap, newSnap)

		want := []ybTabletDefect{{identity: ybTabletIdentity(fixture.remaps[2]), carried: false, missing: 0}}
		if !slices.Equal(defects, want) {
			t.Fatalf("defects = %+v, want the uncarried tablet %+v", defects, want)
		}
		if err := ybTabletDefectError(defects, len(fixture.remaps)); !strings.Contains(err.Error(), "no node's archive carried it") {
			t.Fatalf("the failure must say the export never carried it, got: %v", err)
		}
	})
}

// TestYBRestorePlacesEveryTabletTheArchiveCarries proves a whole export passes,
// that the files really land where yugabyted reads them, and that one copy is
// taken rather than several merged: yb1 comes first, so its file set is the one
// that lands and yb2's extra file must not appear.
func TestYBRestorePlacesEveryTabletTheArchiveCarries(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
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
			t.Fatalf("audit = expected %d, placed %d, missing %v; want 4, 4 and none",
				audit.Expected, audit.Placed, audit.Missing)
		}
		if err := ybPlacementVerdict(audit, len(fixture.remaps)); err != nil {
			t.Fatalf("a complete export must pass the drill: %v", err)
		}
		for _, remap := range fixture.remaps {
			placed := fixture.placedTabletDir(remap, newSnap)
			if _, err := os.Stat(filepath.Join(placed, "000123.sst")); err != nil {
				t.Fatalf("tablet %s was counted as placed but its files are not there: %v", remap.new, err)
			}
			if _, err := os.Stat(filepath.Join(placed, "EXTRA")); err == nil {
				t.Fatalf("tablet %s mixed a second node's copy into the copy", remap.new)
			}
		}
	})
}

// TestYBRestoreTakesTheCopyThatMatchesItsInventory proves the choice skips a
// node whose extraction is short and takes one that is whole, so a damaged copy
// on the first node does not hide a good copy on another. yb1 comes first and
// loses a file; yb2 is the copy that must land.
func TestYBRestoreTakesTheCopyThatMatchesItsInventory(t *testing.T) {
	eachPlacementShell(t, func(t *testing.T, shell string) {
		const exportSnap, newSnap = "expsnap", "newsnap"
		fixture := newPlacementFixture(t, shell, 2)
		for _, remap := range fixture.remaps {
			fixture.stageTablet(t, remap, "yb1", exportSnap, "CURRENT", "000123.sst")
			fixture.stageTablet(t, remap, "yb2", exportSnap, "CURRENT", "000123.sst")
		}
		fixture.archive(t, "yb1", exportSnap)
		fixture.archive(t, "yb2", exportSnap)
		for _, remap := range fixture.remaps {
			if err := os.Remove(filepath.Join(fixture.tabletSourceDir(remap, "yb1", exportSnap), "000123.sst")); err != nil {
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
			body, err := os.ReadFile(filepath.Join(fixture.placedTabletDir(remap, newSnap), "000123.sst"))
			if err != nil {
				t.Fatalf("tablet %s was placed but its files are not there: %v", remap.new, err)
			}
			if !strings.HasPrefix(string(body), "yb2:") {
				t.Fatalf("tablet %s was copied from %q, want the copy that matched its inventory", remap.new, body)
			}
		}
	})
}

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
