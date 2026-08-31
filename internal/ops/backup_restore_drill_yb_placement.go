// backup_restore_drill_yb_placement.go copies each exported tablet's files
// into the tablet the import created, and accounts for every one of them. A
// tablet the import expects but the export does not carry is a hole in the
// restore, and until the placement counted what it copied that hole was
// invisible: the copy was guarded by a directory test, a false test is not a
// command failure, so `set -e` never fired and a restore missing most of its
// tablet files still reached restore_snapshot and passed the row assertion.
// The clauses now record what they attempt and what they find, and the drill
// fails on any difference. A directory test alone left the same hole open one
// step further in, because a directory that exists and holds nothing passed it,
// so a tablet counts as found only when its source carries data.

package ops

import (
	"fmt"
	"strings"
)

// ybPlacementLayout names the directories and ledgers one placement run works
// against inside the scratch container. It travels as a value so the scripts
// can be built and run against a throwaway tree, which is how the placement's
// behavior is proven rather than its text asserted.
type ybPlacementLayout struct {
	// ExportRoot holds one subdirectory per tablet-server node, each the
	// extraction of that node's archive.
	ExportRoot string
	// RocksDBDir is the destination cluster's rocksdb root, under which the
	// import created the new tablets.
	RocksDBDir string
	// ExpectedLedger records every tablet a clause was built for, written
	// before the copy is attempted.
	ExpectedLedger string
	// PlacedLedger records every tablet whose files were found and copied.
	// The difference between the two ledgers is the set of tablets the export
	// did not carry.
	PlacedLedger string
}

// ybTabletExportRoot is where each node's archive is extracted in the scratch
// container, one directory per node so replicas of the same tablet from
// different nodes never mix files.
const ybTabletExportRoot = "/tmp/exp"

// The placement ledgers are files rather than a growing command line, because
// the accounting has to hold for a corpus of any size.
const (
	ybPlacementExpectedLedger = "/tmp/tack-drill-tablets-expected"
	ybPlacementPlacedLedger   = "/tmp/tack-drill-tablets-placed"
)

// drillPlacementLayout is the layout the drill runs against the scratch
// container, with the rocksdb root coming from configuration.
func drillPlacementLayout(rocksdbDir string) ybPlacementLayout {
	return ybPlacementLayout{
		ExportRoot:     ybTabletExportRoot,
		RocksDBDir:     rocksdbDir,
		ExpectedLedger: ybPlacementExpectedLedger,
		PlacedLedger:   ybPlacementPlacedLedger,
	}
}

// The placement script travels to the container as a single `sh -c` argument,
// and Linux caps one exec argument at 128KiB (MAX_ARG_STRLEN): the 2026-08-30
// QA drill broke that cap once the recreated corpus passed ~440 tablets, so
// scripts are built by measured bytes, never by clause count, because each
// clause embeds the configured rocksdb directory twice and a longer directory
// value would shrink how many clauses fit. Half the kernel cap leaves the
// margin. One clause carries four PATH_MAX (4KiB) values plus constants, and
// quoting adds two bytes to each value plus three per quote character in it, so
// a clause built from values no real deployment produces still fits inside the
// kernel cap; a clause that on its own exceeds the budget below is emitted as
// its own script rather than merged into another.
const ybPlacementScriptMaxBytes = 64 * 1024

// ybPlacementScriptPrefix opens every placement script, so a failed copy stops
// the chunk instead of silently skipping tablets, and binds both ledger paths
// to one-letter names so each clause spends a few bytes on the accounting
// rather than two more absolute paths. The paths are quoted, so a ledger
// directory carrying a space is one word rather than a command name.
func ybPlacementScriptPrefix(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; "
}

// ybPlacementScripts builds the shell scripts that copy each exported tablet's
// files into the tablet the import created, flushing to a new script whenever
// the next clause would push the current one past the byte budget, so no
// script exceeds the kernel's per-argument limit however many tablets the
// corpus holds.
func ybPlacementScripts(remaps []ybTabletRemap, layout ybPlacementLayout, exportSnap, newSnap string) []string {
	prefix := ybPlacementScriptPrefix(layout)
	var scripts []string
	var script strings.Builder
	for _, remap := range remaps {
		clause := ybPlacementClause(remap, layout, exportSnap, newSnap)
		if script.Len() > 0 && script.Len()+len(clause) > ybPlacementScriptMaxBytes {
			scripts = append(scripts, script.String())
			script.Reset()
		}
		if script.Len() == 0 {
			script.WriteString(prefix)
		}
		script.WriteString(clause)
	}
	if script.Len() > 0 {
		scripts = append(scripts, script.String())
	}
	return scripts
}

// ybPlacementClause is the shell for one tablet: record it as expected, copy
// the first replica whose extraction directory actually carries data, and
// record it as placed only once that copy has run. A tablet no node carried
// falls through the loop leaving no placed record, which is what the audit
// reads.
//
// The source test is what a tablet has to pass to count. A directory test alone
// passed a directory that exists and holds nothing, so an archive truncated
// after its directory entries placed every tablet and audited clean while
// carrying none of their contents. The test therefore asks for at least one
// regular file of non-zero size beneath the source. That is a statement about
// bytes rather than about names: it holds whatever the engine calls its files,
// where requiring a named file such as CURRENT would be a guess at a layout
// this repo cannot check without a live cluster. It is the weaker claim, and it
// is the one that is certainly true of a tablet that carries data and certainly
// false of a directory that does not.
func ybPlacementClause(remap ybTabletRemap, layout ybPlacementLayout, exportSnap, newSnap string) string {
	// Only the glob's own `*` sits outside quotes, so every path and id around
	// it is one shell word: a configured directory carrying a space stays one
	// word, and an id carrying $(...) or a backtick stays data. The glob spans
	// the per-node extraction dirs; with replication the same tablet exists
	// under several nodes and any single replica's file set is a consistent
	// copy, so the first match carrying data wins and the loop breaks before
	// another node's replica can mix in.
	srcGlob := shellQuote(layout.ExportRoot) + "/*/" + shellQuote(fmt.Sprintf(
		"table-%s/tablet-%s.snapshots/%s", remap.table, remap.old, exportSnap))
	dst := shellQuote(fmt.Sprintf("%s/table-%s/tablet-%s.snapshots/%s",
		layout.RocksDBDir, remap.table, remap.new, newSnap))
	identity := shellQuote(ybTabletIdentity(remap))
	// cp -a preserves the tablet files' ownership from the export, and the
	// placement exec runs as the container's default user (root in the
	// yugabyted image), matching the rocksdb files yugabyted reads. No chown
	// is needed, and the image has no `yugabyte` user name to chown to.
	// The ledger redirects name "$e" and "$p" in quotes: bash rejects a
	// redirection whose unquoted expansion splits into more than one word as an
	// ambiguous redirect, so an unquoted target loses every placement record the
	// moment a ledger path carries a space.
	return fmt.Sprintf(
		"printf '%%s\\n' %s >>\"$e\"; for src in %s; do if [ -d \"$src\" ] && "+
			"[ -n \"$(find \"$src\" -type f -size +0c | head -n 1)\" ]; then "+
			"mkdir -p %s && cp -a \"$src\"/. %s/ && printf '%%s\\n' %s >>\"$p\"; break; fi; done; ",
		identity, srcGlob, dst, dst, identity)
}

// ybTabletIdentity names one tablet of one table, which is what the ledgers
// compare. The table id is part of it so two tables can never collapse onto
// one ledger line.
func ybTabletIdentity(remap ybTabletRemap) string {
	return remap.table + ":" + remap.new
}

// ybPlacementAuditScript reads both ledgers and reports what the placement
// covered. Its length is fixed whatever the corpus holds, and the set
// difference is computed in the container, so a healthy run answers with two
// numbers rather than one line per tablet.
func ybPlacementAuditScript(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; " +
		`touch "$e" "$p"; sort -u "$e" >"$e.sorted"; sort -u "$p" >"$p.sorted"; ` +
		`printf 'expected %s\nplaced %s\nmissing\n' "$(wc -l <"$e.sorted")" "$(wc -l <"$p.sorted")"; ` +
		`comm -23 "$e.sorted" "$p.sorted"`
}
