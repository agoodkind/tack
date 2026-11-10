// Tablet placement copies each exported tablet's files into the tablet that
// import created and accounts for every tablet. A missing export copy makes the
// restore incomplete. Before placement accounting, a directory test guarded
// the copy. A false test did not trigger `set -e`, and restores missing most
// tablet files could still run restore_snapshot and pass the row assertion.
// The clauses now record expected and completed copies, and the drill rejects a
// difference.
//
// Replica selection checks each node's recorded archive inventory before
// script construction. The shell does not search for or evaluate source copies.
// Previously, the clause accepted a source directory with at least one nonempty
// file. That test also accepted, copied, and counted incomplete tablets.

package ops

import (
	"fmt"
	"strings"
)

// ybPlacementLayout specifies placement directories and ledger files inside the
// scratch container. Tests supply a temporary tree through this value and run
// the scripts to check placement behavior instead of asserting script text.
type ybPlacementLayout struct {
	// ExportRoot contains one directory per tablet-server node with that node's
	// extracted archive.
	ExportRoot string
	// RocksDBDir specifies the destination cluster's rocksdb root, where import
	// created the new tablets.
	RocksDBDir string
	// ExpectedLedger records each tablet before its clause attempts the copy.
	ExpectedLedger string
	// PlacedLedger records each tablet after a successful copy. The difference from
	// ExpectedLedger identifies tablets without a recorded successful placement.
	PlacedLedger string
}

// ybTabletExportRoot specifies the scratch container's archive extraction root.
// Each node uses a separate directory to prevent replicas of the same tablet
// from mixing files.
const ybTabletExportRoot = "/tmp/exp"

// Placement uses ledger files instead of an expanding command line to account
// for a corpus of any size.
const (
	ybPlacementExpectedLedger = "/tmp/tack-drill-tablets-expected"
	ybPlacementPlacedLedger   = "/tmp/tack-drill-tablets-placed"
)

// drillPlacementLayout specifies the scratch container's drill layout and uses
// the configured rocksdb root.
func drillPlacementLayout(rocksdbDir string) ybPlacementLayout {
	return ybPlacementLayout{
		ExportRoot:     ybTabletExportRoot,
		RocksDBDir:     rocksdbDir,
		ExpectedLedger: ybPlacementExpectedLedger,
		PlacedLedger:   ybPlacementPlacedLedger,
	}
}

// Linux limits one exec argument to 128KiB (MAX_ARG_STRLEN). Each placement
// script forms one `sh -c` argument. On 2026-08-30, a QA drill exceeded that
// limit after the recreated corpus grew past approximately 440 tablets.
// Script construction measures bytes rather than clauses: each clause includes
// the configured rocksdb directory twice, and longer paths reduce the number of
// clauses that fit. The budget reserves half the kernel limit as margin.
//
// Each clause includes its source path once, destination path twice, and
// tablet identity twice. Quoting adds two bytes per value and three per quote
// character. Even PATH_MAX (4KiB) values that real deployments do not produce
// fit one clause inside the kernel limit. A clause that exceeds the smaller
// budget forms its own script instead of sharing one.
const ybPlacementScriptMaxBytes = 64 * 1024

// ybPlacementScriptPrefix starts each script with `set -e` and assigns both
// ledger paths to single-letter variables. The clauses use those variables
// instead of repeating absolute paths. Quoting preserves a ledger path with
// spaces as one shell word.
func ybPlacementScriptPrefix(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; "
}

// ybPlacementScripts builds scripts that copy verified tablet sources into
// imported tablets. It starts a new script when the next clause would exceed
// the byte budget. Corpus size therefore changes the number of scripts rather
// than one argument's length. A single clause larger than the budget still
// forms its own script.
func ybPlacementScripts(placements []ybTabletPlacement, layout ybPlacementLayout, newSnap string) []string {
	prefix := ybPlacementScriptPrefix(layout)
	var scripts []string
	var script strings.Builder
	for _, placement := range placements {
		clause := ybPlacementClause(placement, layout, newSnap)
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

// ybPlacementClause records a tablet as expected, copies its verified source
// into the imported tablet's snapshot directory, and records placement after a
// successful copy. Replica selection has already checked the node inventories.
// The clause uses `&&` to require successful directory creation and copying
// before it records placement. The script prefix uses `set -e`. A failed mkdir
// or cp skips the completion record and can permit later clauses to run.
// The later placement audit rejects missing records.
func ybPlacementClause(placement ybTabletPlacement, layout ybPlacementLayout, newSnap string) string {
	// Quote each path and ID as one shell word. A configured path with spaces
	// remains one word, and an ID with $(...) or a backtick remains data.
	src := shellQuote(placement.source)
	dst := shellQuote(fmt.Sprintf("%s/table-%s/tablet-%s.snapshots/%s",
		layout.RocksDBDir, placement.remap.table, placement.remap.new, newSnap))
	identity := shellQuote(ybTabletIdentity(placement.remap))
	// `cp -a` preserves exported tablet file ownership. The original placement
	// comment assumes that placement exec uses the container's default user,
	// that the yugabyted image uses root as that default, and that the image does
	// not provide a `yugabyte` account. Under those assumptions, exported ownership
	// matches the rocksdb files yugabyted reads, and placement does not need chown.
	//
	// Quote the ledger redirect targets "$e" and "$p". Bash rejects an unquoted
	// expansion with multiple words as an ambiguous redirect. A ledger path with
	// spaces would otherwise prevent placement records.
	return fmt.Sprintf(
		"printf '%%s\\n' %s >>\"$e\"; mkdir -p %s && cp -a %s/. %s/ && printf '%%s\\n' %s >>\"$p\"; ",
		identity, dst, src, dst, identity)
}

// ybTabletIdentity combines the table ID and new tablet ID for ledger
// comparisons. Including the table ID prevents tablets from different tables
// from sharing one ledger identity.
func ybTabletIdentity(remap ybTabletRemap) string {
	return remap.table + ":" + remap.new
}

// ybPlacementAuditScript compares both ledgers inside the container. The script
// length does not depend on corpus size. A complete run reports two counts
// instead of a line for every tablet.
func ybPlacementAuditScript(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; " +
		`touch "$e" "$p"; sort -u "$e" >"$e.sorted"; sort -u "$p" >"$p.sorted"; ` +
		`printf 'expected %s\nplaced %s\nmissing\n' "$(wc -l <"$e.sorted")" "$(wc -l <"$p.sorted")"; ` +
		`comm -23 "$e.sorted" "$p.sorted"`
}
