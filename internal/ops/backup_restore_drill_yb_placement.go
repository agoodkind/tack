// Tablet placement copies verified exports into the tablets that the import
// created. Each clause records its tablet before copying and records completion
// after copying. The audit compares distinct identities from both records.
// Replica selection checks archive inventories before script creation.

package ops

import (
	"fmt"
	"strings"
)

// ybPlacementLayout specifies directories and ledger paths inside the scratch
// container. Tests use separate directories to execute the generated programs.
type ybPlacementLayout struct {
	// ExportRoot contains each node's extracted archive in a separate directory.
	ExportRoot string
	// RocksDBDir specifies the destination root where the import created tablets.
	RocksDBDir string
	// ExpectedLedger specifies the file where clauses record attempted copies.
	ExpectedLedger string
	// PlacedLedger specifies the file where clauses record completed copies.
	PlacedLedger string
}

// ybTabletExportRoot separates each node's extracted archive to prevent copies
// from combining files from different replicas.
const ybTabletExportRoot = "/tmp/exp"

// Placement records use files rather than adding each identity to an exec
// argument. The audit reads these files regardless of the tablet count.
const (
	ybPlacementExpectedLedger = "/tmp/tack-drill-tablets-expected"
	ybPlacementPlacedLedger   = "/tmp/tack-drill-tablets-placed"
)

// drillPlacementLayout uses the configured RocksDB root for the scratch drill.
func drillPlacementLayout(rocksdbDir string) ybPlacementLayout {
	return ybPlacementLayout{
		ExportRoot:     ybTabletExportRoot,
		RocksDBDir:     rocksdbDir,
		ExpectedLedger: ybPlacementExpectedLedger,
		PlacedLedger:   ybPlacementPlacedLedger,
	}
}

// ybPlacementScriptMaxBytes limits combined clauses by bytes rather than count.
// Each clause repeats configured paths, and quoting increases their size.
// A clause that exceeds this budget runs in a separate script.
const ybPlacementScriptMaxBytes = 64 * 1024

// ybPlacementScriptPrefix enables shell failure handling and assigns quoted
// ledger paths to variables that each clause uses for its records.
func ybPlacementScriptPrefix(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; "
}

// ybPlacementScripts preserves placement order and starts a new script when
// adding the next clause would exceed the byte budget.
func ybPlacementScripts(
	placements []ybTabletPlacement,
	layout ybPlacementLayout,
	newSnap string,
) []string {
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

// ybPlacementClause records an attempt, copies the verified source into the
// new snapshot directory, and records completion only after the copy succeeds.
func ybPlacementClause(
	placement ybTabletPlacement,
	layout ybPlacementLayout,
	newSnap string,
) string {
	// Quoting preserves literal paths and prevents shell expansion of IDs.
	src := shellQuote(placement.source)
	dst := shellQuote(
		fmt.Sprintf(
			"%s/table-%s/tablet-%s.snapshots/%s",
			layout.RocksDBDir,
			placement.remap.table,
			placement.remap.new,
			newSnap,
		),
	)
	identity := shellQuote(ybTabletIdentity(placement.remap))
	// cp -a preserves ownership from the export. The container's default user
	// runs the copy. Quoted redirection targets preserve spaces in ledger paths.
	return fmt.Sprintf(
		"printf '%%s\\n' %s >>\"$e\"; mkdir -p %s && cp -a %s/. %s/ && printf '%%s\\n' %s >>\"$p\"; ",
		identity,
		dst,
		src,
		dst,
		identity,
	)
}

// ybTabletIdentity combines table and tablet IDs for ledger comparisons.
func ybTabletIdentity(remap ybTabletRemap) string {
	return remap.table + ":" + remap.new
}

// ybPlacementAuditScript creates absent ledgers without changing existing
// contents. It counts distinct identities and lists attempted copies that did
// not complete. Ledger timestamps do not affect these comparisons.
func ybPlacementAuditScript(layout ybPlacementLayout) string {
	return "set -e; e=" + shellQuote(layout.ExpectedLedger) +
		"; p=" + shellQuote(layout.PlacedLedger) + "; " +
		`if [ ! -e "$e" ]; then : >>"$e"; fi; if [ ! -e "$p" ]; then : >>"$p"; fi; ` +
		`sort -u "$e" >"$e.sorted"; sort -u "$p" >"$p.sorted"; ` +
		`printf 'expected %s\nplaced %s\nmissing\n' "$(wc -l <"$e.sorted")" "$(wc -l <"$p.sorted")"; ` +
		`comm -23 "$e.sorted" "$p.sorted"`
}
