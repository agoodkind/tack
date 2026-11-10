package ops

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// maxExecArgBytes specifies Linux's per-argument exec limit, MAX_ARG_STRLEN,
// at 32 pages. A placement script at or above the limit fails container exec
// with "argument list too long", as the 2026-08-30 QA drill did.
const maxExecArgBytes = 128 * 1024

// TestYBPlacementScriptsStayUnderTheExecArgLimit uses a corpus much larger
// than the approximately 440 tablets that broke single-script placement.
// It checks each script's byte limit and shared prefix, the total clause count,
// and inclusion of the first and last remap. The long-directory case checks
// byte measurement: each clause includes the configured directory twice, and
// fixed clause counts could exceed the limit as paths grow. Removing chunking
// makes the size assertion fail.
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

// TestYBPlacementScriptsEmpty checks that zero placements produce zero scripts
// and cannot cause an empty placement exec.
func TestYBPlacementScriptsEmpty(t *testing.T) {
	if scripts := ybPlacementScripts(nil, drillPlacementLayout("/data"), "new"); len(scripts) != 0 {
		t.Fatalf("scripts = %d, want none for zero placements", len(scripts))
	}
}

// TestChooseYBTabletReplicasDecidesEachTabletOnce checks duplicate mapping
// rows. A repeated complete tablet produces one placement, and a repeated
// absent tablet produces one refusal. The audit counts distinct tablets;
// duplicate placements would overstate attempts, and duplicate refusals would
// overstate missing tablets.
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
