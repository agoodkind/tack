package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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

// placedTabletDir is where the placement should have put one tablet's files.
func (f *placementFixture) placedTabletDir(remap ybTabletRemap, newSnap string) string {
	return filepath.Join(f.layout.RocksDBDir,
		"table-"+remap.table, "tablet-"+remap.new+".snapshots", newSnap)
}
