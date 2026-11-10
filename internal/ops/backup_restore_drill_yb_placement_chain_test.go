package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// placementShells lists the shells for checking generated scripts. The drill
// runs scripts with `sh`, which uses dash in some images and bash in others.
// Bash rejects redirects when unquoted expansion produces multiple words.
// A script that dash accepts can lose every placement record under bash.
var placementShells = []string{"sh", "bash"}

// archive creates one node's archive with the export's tar command and
// reads the inventory from that archive. It stages both artifacts as the
// drill does and unpacks the archive with the drill's extraction program.
// Later test damage to extracted files leaves the recorded inventory unchanged,
// matching the failure that the drill must detect.
func (f *placementFixture) archive(t *testing.T, node, exportSnap string) ybArchiveInventory {
	t.Helper()
	if err := os.MkdirAll(f.artifactDir, 0o755); err != nil {
		t.Fatalf("make the fixture staging dir: %v", err)
	}
	tarPath := filepath.Join(f.artifactDir, ybDrillArchiveName(node))
	// COPYFILE_DISABLE prevents macOS bsdtar from adding AppleDouble "._name"
	// members for entries. Linux export containers do not produce those members.
	// Without this setting, the fixture inventory would include files that
	// extraction does not create.
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

// extract runs the drill's extraction program on one staged node archive.
// The fixture staging directory replaces the container's read-only /artifacts
// mount without changing the program.
func (f *placementFixture) extract(t *testing.T, node string) {
	t.Helper()
	runShellArgs(t, f.shell, ybTabletExtractScript(f.layout.ExportRoot, f.artifactDir), node)
}

// check runs the generated comparison program against the inventory beside one
// node's archive and returns the files missing from extraction.
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

// restore checks each extraction against its node's inventory, selects a copy
// per tablet, and places those copies. The audit applies only when selection
// accepts every tablet. Production stops before placement if selection rejects
// a tablet.
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

// runShell executes a generated script as the drill does and fails the test if
// the script returns an error.
func runShell(t *testing.T, shell, script string) string {
	t.Helper()
	return runShellArgs(t, shell, script)
}

// runShellArgs runs a generated script with positional parameters. The
// invocation supplies the program, the shell name as $0, and then the parameter
// values, matching the drill.
func runShellArgs(t *testing.T, shell, script string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", script, shell}, args...)
	out, err := exec.CommandContext(t.Context(), shell, argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s script failed: %v\n%s", shell, err, out)
	}
	return string(out)
}

// eachPlacementShell runs body once per required shell. It reports an
// unavailable shell instead of silently skipping it; skipping does not
// establish script behavior under that shell.
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
