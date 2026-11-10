package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// placementShells are the shells the generated scripts are proven against. The
// drill hands them to `sh`, which is dash on some images and bash on others,
// and the two disagree on enough to matter: bash refuses a redirection whose
// unquoted expansion splits into more than one word, so a script dash accepts
// can lose every placement record under bash.
var placementShells = []string{"sh", "bash"}

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
