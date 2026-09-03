package ops

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// maxExecArgBytes is the Linux per-argument exec limit (MAX_ARG_STRLEN,
// 32 pages). A placement script at or past it makes the container exec fail
// with "argument list too long", which is the 2026-08-30 QA drill failure.
const maxExecArgBytes = 128 * 1024

// TestYBPlacementScriptsStayUnderTheExecArgLimit proves a corpus far past the
// size that broke the single-script placement (~440 tablets) yields scripts
// that each fit in one exec argument, together cover every remap exactly once,
// and keep the fail-fast prefix. The long-directory case is why the chunks are
// measured in bytes: each clause embeds the directory twice, so a count-based
// chunk would grow past the limit as the configured path grows. With the
// chunking removed (one script for all remaps), the size assertion fails,
// which is the red proof this test exists for.
func TestYBPlacementScriptsStayUnderTheExecArgLimit(t *testing.T) {
	const tabletCount = 5000
	const exportSnap = "e3e4ff5d-cf6c-42a6-93a2-1518823d5a86"
	placements := make([]ybTabletPlacement, 0, tabletCount)
	for i := range tabletCount {
		remap := ybTabletRemap{
			table: fmt.Sprintf("0000400000003000800000000000%04x", i),
			old:   fmt.Sprintf("%08x-1111-2222-3333-444455556666", i),
			new:   fmt.Sprintf("%08x-aaaa-bbbb-cccc-ddddeeeeffff", i),
		}
		placements = append(placements, ybTabletPlacement{
			remap:  remap,
			source: ybTabletExportRoot + "/yb1/" + ybTabletSourceDir(remap, exportSnap),
		})
	}

	for _, tc := range []struct {
		name       string
		rocksdbDir string
	}{
		{name: "deployed dir", rocksdbDir: "/home/yugabyte/var/data/yb-data/tserver/data/rocksdb"},
		{name: "pathological long dir", rocksdbDir: "/" + strings.Repeat("d", 4000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout := drillPlacementLayout(tc.rocksdbDir)
			scripts := ybPlacementScripts(placements, layout, "80086029-2a1e-4625-bad1-3ea00e4109a6")

			placed := 0
			for i, script := range scripts {
				if len(script) >= maxExecArgBytes {
					t.Fatalf("script %d is %d bytes, at or past the %d byte exec argument limit",
						i, len(script), maxExecArgBytes)
				}
				if !strings.HasPrefix(script, ybPlacementScriptPrefix(layout)) {
					t.Fatalf("script %d lost the fail-fast prefix: %q", i, script[:20])
				}
				placed += strings.Count(script, "mkdir -p ")
			}
			if placed != tabletCount {
				t.Fatalf("scripts place %d tablets, want %d", placed, tabletCount)
			}
			for _, placement := range []ybTabletPlacement{placements[0], placements[tabletCount-1]} {
				found := false
				for _, script := range scripts {
					if strings.Contains(script, "tablet-"+placement.remap.new+".snapshots") {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("no script places tablet %s", placement.remap.new)
				}
			}
		})
	}
}

// TestYBPlacementScriptsEmpty locks that zero placements produce zero scripts,
// so the drill cannot run an empty placement exec.
func TestYBPlacementScriptsEmpty(t *testing.T) {
	if scripts := ybPlacementScripts(nil, drillPlacementLayout("/data"), "new"); len(scripts) != 0 {
		t.Fatalf("scripts = %d, want none for zero placements", len(scripts))
	}
}

// TestChooseYBTabletReplicasDecidesEachTabletOnce proves a mapping that names
// one tablet twice yields one copy and one refusal, not two. The audit counts
// tablets, so a doubled placement would attempt more than the import created
// and a doubled refusal would claim the export is short of more than it holds.
func TestChooseYBTabletReplicasDecidesEachTabletOnce(t *testing.T) {
	const exportSnap = "expsnap"
	whole := ybTabletRemap{table: "t1", old: "o1", new: "n1"}
	absent := ybTabletRemap{table: "t2", old: "o2", new: "n2"}
	remaps := []ybTabletRemap{whole, whole, absent, absent}
	nodes := []ybNodeExtraction{{
		node:    "yb1",
		carried: map[string]int{ybTabletSourceDir(whole, exportSnap): 2},
		missing: map[string]int{},
	}}

	placements, defects := chooseYBTabletReplicas(remaps, "/tmp/exp", exportSnap, nodes)

	if len(placements) != 1 || placements[0].source != "/tmp/exp/yb1/"+ybTabletSourceDir(whole, exportSnap) {
		t.Fatalf("placements = %+v, want the one whole tablet once", placements)
	}
	want := []ybTabletDefect{{identity: ybTabletIdentity(absent), carried: false, missing: 0}}
	if !slices.Equal(defects, want) {
		t.Fatalf("defects = %+v, want the uncarried tablet once %+v", defects, want)
	}
}
