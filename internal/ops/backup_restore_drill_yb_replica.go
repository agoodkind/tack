// backup_restore_drill_yb_replica.go picks which node's extracted copy of a
// tablet the placement copies, and refuses the restore when no node has a whole
// one. A tablet exists on several nodes and any one node's copy is a consistent
// set, so the choice is made against the inventories: a copy counts only when
// every file that node's archive recorded for that tablet is in the extraction
// at its recorded size. Deciding here rather than in the placement shell is
// what lets the refusal name the tablet and how many of its files are gone.

package ops

import (
	"fmt"
	"strings"
)

// ybTabletDirComponents is how many path components a tablet's directory has
// inside an archive: "table-<id>/tablet-<id>.snapshots/<snapshot>".
const ybTabletDirComponents = 3

// ybTabletSourceDir is the archive-relative directory one tablet's exported
// files live under, named by the tablet id the export knew it as.
func ybTabletSourceDir(remap ybTabletRemap, exportSnap string) string {
	return fmt.Sprintf("table-%s/tablet-%s.snapshots/%s", remap.table, remap.old, exportSnap)
}

// ybTabletDirOf reports which tablet directory an archive-relative file path
// belongs to, or "" for a path too shallow to be a tablet's file. Files nested
// deeper still belong to the tablet directory above them.
func ybTabletDirOf(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= ybTabletDirComponents {
		return ""
	}
	return strings.Join(parts[:ybTabletDirComponents], "/")
}

// ybNodeExtraction is one node's extracted archive weighed against its
// inventory, counted per tablet directory.
type ybNodeExtraction struct {
	// node is the tablet-server name whose archive this is.
	node string
	// carried counts the files the inventory records for each tablet.
	carried map[string]int
	// missing counts those the extraction does not hold at their recorded size.
	missing map[string]int
}

// newYBNodeExtraction folds one node's inventory and the files its extraction
// is short of into per-tablet counts.
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

// ybTabletPlacement pairs one tablet with the extracted copy the drill verified
// against the inventory of the node that carried it.
type ybTabletPlacement struct {
	remap  ybTabletRemap
	source string
}

// ybTabletDefect is one tablet the export cannot supply: either no node's
// archive carried it, or every node that did is short of files.
type ybTabletDefect struct {
	// identity names the tablet the way the placement ledgers do.
	identity string
	// carried is whether any node's archive recorded files for it at all.
	carried bool
	// missing is the fewest files any carrying node's extraction is short of.
	missing int
}

// chooseYBTabletReplicas takes, for each tablet the import created, the first
// node whose extraction holds every file that node archived for it. Nodes are
// walked in manifest order, so the choice is the same on every run over the
// same export. A tablet named twice by the mapping is decided once, under the
// identity the ledgers record, so neither the copies nor the refusals count it
// twice.
func chooseYBTabletReplicas(
	remaps []ybTabletRemap,
	exportRoot, exportSnap string,
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

// chooseYBTabletReplica returns the whole copy of one tablet, or the defect
// describing the best any node could offer.
func chooseYBTabletReplica(
	remap ybTabletRemap,
	dir, exportRoot string,
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

// ybTabletDefectError refuses a restore the export cannot supply, naming what
// is wrong with each tablet it cannot place.
func ybTabletDefectError(defects []ybTabletDefect, wanted int) error {
	reasons := make([]string, 0, len(defects))
	for _, defect := range defects {
		reasons = append(reasons, defect.reason())
	}
	return fmt.Errorf("the export cannot supply %d of the %d tablets the import created: %s",
		len(defects), wanted, sampleTabletIdentities(reasons))
}

// reason says why one tablet cannot be placed.
func (d ybTabletDefect) reason() string {
	if !d.carried {
		return d.identity + " (no node's archive carried it)"
	}
	return fmt.Sprintf("%s (%d archived files are missing from every node that carried it)",
		d.identity, d.missing)
}
