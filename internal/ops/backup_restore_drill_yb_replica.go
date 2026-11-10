// Tablet replica selection chooses one node's extracted copy for placement
// and refuses a restore if every copy is incomplete. A tablet can have copies
// on several nodes; each node supplies a consistent file set. A complete copy
// contains every file in that node's inventory at its recorded size. Selection
// before shell placement permits an error with the tablet's identity and
// missing-file count.

package ops

import (
	"fmt"
	"strings"
)

// ybTabletDirComponents counts the components in an archived tablet
// directory: "table-<id>/tablet-<id>.snapshots/<snapshot>".
const ybTabletDirComponents = 3

// ybTabletSourceDir returns the archive-relative directory for a tablet's
// exported files, using the tablet ID from the export.
func ybTabletSourceDir(remap ybTabletRemap, exportSnap string) string {
	return fmt.Sprintf("table-%s/tablet-%s.snapshots/%s", remap.table, remap.old, exportSnap)
}

// ybTabletDirOf returns the tablet directory for an archive-relative file
// path. It returns an empty string for a path without a file below that
// directory. Deeper files use the tablet directory above them.
func ybTabletDirOf(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= ybTabletDirComponents {
		return ""
	}

	return strings.Join(parts[:ybTabletDirComponents], "/")
}

// ybNodeExtraction records one node's inventory and incomplete-file counts
// by tablet directory.
type ybNodeExtraction struct {
	// node identifies the tablet server that produced the archive.
	node string
	// carried counts the files the inventory records for each tablet.
	carried map[string]int
	// missing counts absent files and files with a different size from the
	// inventory.
	missing map[string]int
}

// newYBNodeExtraction counts inventoried files and extraction shortfalls
// for each tablet directory.
func newYBNodeExtraction(inventory ybArchiveInventory, missing []ybArchivedFile) ybNodeExtraction {
	extraction := ybNodeExtraction{
		node:    inventory.Node,
		carried: map[string]int{},
		missing: map[string]int{},
	}
	for _, file := range inventory.Files {
		if dir := ybTabletDirOf(file.Path); dir != "" {
			extraction.carried[dir]++
		}
	}

	for _, file := range missing {
		if dir := ybTabletDirOf(file.Path); dir != "" {
			extraction.missing[dir]++
		}
	}

	return extraction
}

// ybTabletPlacement pairs a tablet with an extracted copy that matches the
// source node's inventory.
type ybTabletPlacement struct {
	remap  ybTabletRemap
	source string
}

// ybTabletDefect records a tablet without a complete source copy. Either
// every node lacks archived files for it, or every available extraction lacks
// files at their recorded sizes.
type ybTabletDefect struct {
	// identity identifies the tablet using the placement ledger's format.
	identity string
	// carried reports whether any node's archive recorded files for the tablet.
	carried bool
	// missing counts the fewest incomplete files among nodes with archived files.
	missing int
}

// chooseYBTabletReplicas selects the first complete extraction for each
// imported tablet, using the manifest's node order. Repeated mapping entries
// use the placement ledger's tablet identity for one decision. The result
// counts each tablet once, either as a copy or as a defect.
func chooseYBTabletReplicas(
	remaps []ybTabletRemap,
	exportRoot string,
	exportSnap string,
	nodes []ybNodeExtraction,
) (placements []ybTabletPlacement, defects []ybTabletDefect) {
	decided := make(map[string]bool, len(remaps))
	for _, remap := range remaps {
		identity := ybTabletIdentity(remap)
		if decided[identity] {
			continue
		}

		decided[identity] = true
		dir := ybTabletSourceDir(remap, exportSnap)
		source, defect := chooseYBTabletReplica(remap, dir, exportRoot, nodes)
		if source != "" {
			placements = append(placements, ybTabletPlacement{remap: remap, source: source})
			continue
		}

		defects = append(defects, defect)
	}

	return placements, defects
}

// chooseYBTabletReplica returns one tablet's complete source copy or a defect.
// The defect records the smallest file shortfall among nodes that archived the
// tablet, or reports that every node lacks archived files for it.
func chooseYBTabletReplica(
	remap ybTabletRemap,
	dir string,
	exportRoot string,
	nodes []ybNodeExtraction,
) (string, ybTabletDefect) {
	defect := ybTabletDefect{identity: ybTabletIdentity(remap), carried: false, missing: 0}

	for _, node := range nodes {
		if node.carried[dir] == 0 {
			continue
		}

		missing := node.missing[dir]
		if missing == 0 {
			return exportRoot + "/" + node.node + "/" + dir, defect
		}

		if !defect.carried || missing < defect.missing {
			defect = ybTabletDefect{identity: defect.identity, carried: true, missing: missing}
		}
	}

	return "", defect
}

// ybTabletDefectError rejects a restore that lacks complete source copies.
// It reports exact counts and a sample of the tablets' defects.
func ybTabletDefectError(defects []ybTabletDefect, wanted int) error {
	reasons := make([]string, 0, len(defects))
	for _, defect := range defects {
		reasons = append(reasons, defect.reason())
	}

	return fmt.Errorf(
		"the export cannot supply %d of the %d tablets the import created: %s",
		len(defects),
		wanted,
		sampleTabletIdentities(reasons),
	)
}

// reason explains why placement cannot copy one tablet.
func (d ybTabletDefect) reason() string {
	if !d.carried {
		return d.identity + " (no node's archive carried it)"
	}

	return fmt.Sprintf(
		"%s (%d archived files are missing from every node that carried it)",
		d.identity,
		d.missing,
	)
}
