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

// placementFixture is a throwaway export tree and destination root laid out
// the way the scratch container's is, on the local filesystem, so the
// generated scripts can be run for real. Asserting their text would prove
// nothing about the case this file exists for: a copy guarded by a directory
// test that is simply never taken.
type placementFixture struct {
	layout ybPlacementLayout
	remaps []ybTabletRemap
}

func newPlacementFixture(t *testing.T, tabletCount int) *placementFixture {
	t.Helper()
	root := t.TempDir()
	remaps := make([]ybTabletRemap, 0, tabletCount)
	for i := range tabletCount {
		remaps = append(remaps, ybTabletRemap{
			table: fmt.Sprintf("table%02d", i),
			old:   fmt.Sprintf("old%02d", i),
			new:   fmt.Sprintf("new%02d", i),
		})
	}
	return &placementFixture{
		layout: ybPlacementLayout{
			ExportRoot:     filepath.Join(root, "exp"),
			RocksDBDir:     filepath.Join(root, "rocksdb"),
			ExpectedLedger: filepath.Join(root, "expected"),
			PlacedLedger:   filepath.Join(root, "placed"),
		},
		remaps: remaps,
	}
}

// stageTablet writes one exported tablet's files under a node's extraction
// directory, which is what a complete export leaves behind.
func (f *placementFixture) stageTablet(t *testing.T, remap ybTabletRemap, node, exportSnap string, files ...string) {
	t.Helper()
	dir := filepath.Join(f.layout.ExportRoot, node,
		"table-"+remap.table, "tablet-"+remap.old+".snapshots", exportSnap)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("stage tablet %s: %v", remap.old, err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(node), 0o600); err != nil {
			t.Fatalf("write tablet file %s: %v", name, err)
		}
	}
}

// place runs every generated placement script and then the audit script in a
// real shell, and returns what the audit read.
func (f *placementFixture) place(t *testing.T, exportSnap, newSnap string) ybPlacementAudit {
	t.Helper()
	for _, script := range ybPlacementScripts(f.remaps, f.layout, exportSnap, newSnap) {
		runShell(t, script)
	}
	out := runShell(t, ybPlacementAuditScript(f.layout))
	audit, err := parseYBPlacementAudit(out)
	if err != nil {
		t.Fatalf("parse placement audit %q: %v", out, err)
	}
	return audit
}

// runShell runs one generated script the way the drill hands it to the
// container, and fails the test if the script itself errors.
func runShell(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestYBPlacementFailsWhenTheExportIsMissingTablets proves the drill now
// refuses a restore whose export does not carry every tablet the import
// created. Before the placement kept ledgers, the copy for a missing tablet
// was a directory test that was simply not taken, `set -e` never fired, and
// the drill went on to restore_snapshot and passed.
func TestYBPlacementFailsWhenTheExportIsMissingTablets(t *testing.T) {
	const exportSnap, newSnap = "expsnap", "newsnap"
	fixture := newPlacementFixture(t, 5)
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
}

// TestYBPlacementPlacesEveryTabletTheExportCarries proves a complete export
// passes, that the files really land where yugabyted reads them, and that one
// replica is copied rather than several merged: yb1 sorts first, so its file
// set is the one that lands and yb2's extra file must not appear.
func TestYBPlacementPlacesEveryTabletTheExportCarries(t *testing.T) {
	const exportSnap, newSnap = "expsnap", "newsnap"
	fixture := newPlacementFixture(t, 4)
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
		placed := filepath.Join(fixture.layout.RocksDBDir,
			"table-"+remap.table, "tablet-"+remap.new+".snapshots", newSnap)
		if _, err := os.Stat(filepath.Join(placed, "CURRENT")); err != nil {
			t.Fatalf("tablet %s was counted as placed but its files are not there: %v", remap.new, err)
		}
		if _, err := os.Stat(filepath.Join(placed, "EXTRA")); err == nil {
			t.Fatalf("tablet %s mixed a second node's replica into the copy", remap.new)
		}
	}
}
