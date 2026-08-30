package ops

import (
	"fmt"
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
// and keep the fail-fast prefix. With the chunking removed (one script for all
// remaps), the size assertion fails, which is the red proof this test exists
// for.
func TestYBPlacementScriptsStayUnderTheExecArgLimit(t *testing.T) {
	const tabletCount = 5000
	remaps := make([]ybTabletRemap, 0, tabletCount)
	for i := range tabletCount {
		remaps = append(remaps, ybTabletRemap{
			table: fmt.Sprintf("0000400000003000800000000000%04x", i),
			old:   fmt.Sprintf("%08x-1111-2222-3333-444455556666", i),
			new:   fmt.Sprintf("%08x-aaaa-bbbb-cccc-ddddeeeeffff", i),
		})
	}

	scripts := ybPlacementScripts(remaps, "/home/yugabyte/var/data/yb-data/tserver/data/rocksdb",
		"e3e4ff5d-cf6c-42a6-93a2-1518823d5a86", "80086029-2a1e-4625-bad1-3ea00e4109a6")

	placed := 0
	for i, script := range scripts {
		if len(script) >= maxExecArgBytes {
			t.Fatalf("script %d is %d bytes, at or past the %d byte exec argument limit", i, len(script), maxExecArgBytes)
		}
		if !strings.HasPrefix(script, "set -e; ") {
			t.Fatalf("script %d lost the fail-fast prefix: %q", i, script[:20])
		}
		placed += strings.Count(script, "for src in ")
	}
	if placed != tabletCount {
		t.Fatalf("scripts place %d tablets, want %d", placed, tabletCount)
	}
	for _, m := range []ybTabletRemap{remaps[0], remaps[tabletCount-1]} {
		found := false
		for _, script := range scripts {
			if strings.Contains(script, "tablet-"+m.new+".snapshots") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no script places tablet %s", m.new)
		}
	}
}

// TestYBPlacementScriptsEmpty locks that zero remaps produce zero scripts, so
// the drill cannot run an empty placement exec.
func TestYBPlacementScriptsEmpty(t *testing.T) {
	if scripts := ybPlacementScripts(nil, "/data", "old", "new"); len(scripts) != 0 {
		t.Fatalf("scripts = %d, want none for zero remaps", len(scripts))
	}
}
