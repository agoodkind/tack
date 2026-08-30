// backup_restore_drill_yb_placement.go copies each exported tablet's files
// into the tablet the import created, and accounts for every one of them. A
// tablet the import expects but the export does not carry is a hole in the
// restore, and until the placement counted what it copied that hole was
// invisible: the copy is guarded by a directory test, a false test is not a
// command failure, so `set -e` never fired and a restore missing most of its
// tablet files still reached restore_snapshot and passed the row assertion.
// The clauses now record what they attempt and what they find, and the drill
// fails on any difference.

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
// margin; one clause is bounded by two PATH_MAX (4KiB) paths plus constants,
// so a single clause can never exceed the budget on its own.
const ybPlacementScriptMaxBytes = 64 * 1024

// ybPlacementScriptPrefix opens every placement script, so a failed copy stops
// the chunk instead of silently skipping tablets, and binds both ledger paths
// to one-letter names so each clause spends a few bytes on the accounting
// rather than two more absolute paths.
func ybPlacementScriptPrefix(layout ybPlacementLayout) string {
	return "set -e; e=" + layout.ExpectedLedger + "; p=" + layout.PlacedLedger + "; "
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
// the first replica any node's extraction directory holds, and record it as
// placed only once that copy has run. A tablet with no source directory falls
// through the loop leaving no placed record, which is what the audit reads.
func ybPlacementClause(remap ybTabletRemap, layout ybPlacementLayout, exportSnap, newSnap string) string {
	// The glob spans the per-node extraction dirs; with replication the same
	// tablet exists under several nodes and any single replica's file set is a
	// consistent copy, so the first match wins and the loop breaks before
	// another node's replica can mix in.
	srcGlob := fmt.Sprintf("%s/*/table-%s/tablet-%s.snapshots/%s",
		layout.ExportRoot, remap.table, remap.old, exportSnap)
	dst := fmt.Sprintf("%s/table-%s/tablet-%s.snapshots/%s",
		layout.RocksDBDir, remap.table, remap.new, newSnap)
	identity := ybTabletIdentity(remap)
	// cp -a preserves the tablet files' ownership from the export, and the
	// placement exec runs as the container's default user (root in the
	// yugabyted image), matching the rocksdb files yugabyted reads. No chown
	// is needed, and the image has no `yugabyte` user name to chown to.
	return fmt.Sprintf(
		"printf '%%s\\n' %q >>$e; for src in %s; do if [ -d \"$src\" ]; then mkdir -p %q && cp -a \"$src\"/. %q/ && printf '%%s\\n' %q >>$p; break; fi; done; ",
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
	return "set -e; e=" + layout.ExpectedLedger + "; p=" + layout.PlacedLedger + "; " +
		`touch "$e" "$p"; sort -u "$e" >"$e.sorted"; sort -u "$p" >"$p.sorted"; ` +
		`printf 'expected %s\nplaced %s\nmissing\n' "$(wc -l <"$e.sorted")" "$(wc -l <"$p.sorted")"; ` +
		`comm -23 "$e.sorted" "$p.sorted"`
}
