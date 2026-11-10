package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// placementFixture builds temporary data-guest and scratch-container trees on
// the local filesystem. It creates archives with the export's tar command,
// reads inventories from the archives, extracts them with the drill's program,
// and runs the generated inventory checks, placement scripts, and audit scripts
// in a real shell. These tests inspect files after archiving and extraction;
// script text assertions cannot establish that behavior.
type placementFixture struct {
	layout ybPlacementLayout
	remaps []ybTabletRemap
	shell  string
	// stageRoot models a data guest's rocksdb root with one directory per node's
	// staged tablets. Extraction uses a separate tree, and checks inspect files
	// from the archive instead of the original staged files.
	stageRoot string
	// artifactDir models the drill staging directory that the scratch container
	// mounts read-only at /artifacts.
	artifactDir string
	// runID identifies the export run in fixture inventories.
	runID string
	// inventories records each node archive's contents in archive order. Replica
	// selection prefers copies in that order.
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

// newPlacementFixtureFor creates a fixture with the supplied root and remaps.
// Tests can select a configured path or tablet ID for placement.
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

// tabletSourceDir returns the directory for one tablet in one node's
// extraction. A complete archive creates the source files that placement copies
// from this directory.
func (f *placementFixture) tabletSourceDir(remap ybTabletRemap, node, exportSnap string) string {
	return filepath.Join(f.layout.ExportRoot, node, ybTabletSourceDir(remap, exportSnap))
}

// stageTablet writes tablet files under one node's rocksdb root for its archive
// command. Each file contains its node and file name. Tests can identify the
// selected node. Copies of one file differ in size when the node names have
// different lengths.
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

// placedTabletDir returns the expected destination for one tablet's files.
func (f *placementFixture) placedTabletDir(remap ybTabletRemap, newSnap string) string {
	return filepath.Join(f.layout.RocksDBDir,
		"table-"+remap.table, "tablet-"+remap.new+".snapshots", newSnap)
}
