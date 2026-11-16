// Tablet placement selects one complete extracted replica per imported tablet.
// A replica must contain every inventoried file at its archived size; files
// from different nodes cannot establish a consistent single-node copy.

package ops

import (
	"fmt"
	"strings"
)

// ybTabletDirComponents counts "table-<id>/tablet-<id>.snapshots/<snapshot>".
const ybTabletDirComponents = 3

// ybTabletSourceDir returns the archive-relative directory for the old
// tablet ID.
func ybTabletSourceDir(remap ybTabletRemap, exportSnap string) string {
	return fmt.Sprintf("table-%s/tablet-%s.snapshots/%s", remap.table, remap.old, exportSnap)
}

// ybTabletDirOf returns the first three path components for a tablet file,
// including nested files, or an empty string for shallower paths.
func ybTabletDirOf(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= ybTabletDirComponents {
		return ""
	}

	return strings.Join(parts[:ybTabletDirComponents], "/")
}

// ybNodeExtraction counts inventoried files and extraction defects per tablet.
type ybNodeExtraction struct {
	// node identifies the archive's tablet server.
	node string
	// carried counts inventoried files per tablet.
	carried map[string]int
	// missing counts absent files and files with incorrect sizes.
	missing map[string]int
}

// newYBNodeExtraction groups inventory and extraction defects by tablet.
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

// ybTabletPlacement pairs an imported tablet with a verified extracted replica.
type ybTabletPlacement struct {
	remap  ybTabletRemap
	source string
}

// ybTabletDefect records an absent or incomplete tablet replica.
type ybTabletDefect struct {
	// identity uses the placement records' tablet identity.
	identity string
	// carried indicates whether any archive includes files for the tablet.
	carried bool
	// missing records the fewest defective files in any inventoried replica.
	missing int
}

// chooseYBTabletReplicas selects the first complete node extraction in manifest
// order. Duplicate imported identities count once for placement and refusal.
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

// chooseYBTabletReplica returns a complete replica or the smallest defect count
// among nodes that archived the tablet.
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

// ybTabletDefectError reports the count and sampled reasons for unplaceable
// tablets.
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

// reason reports why the export cannot provide a complete tablet replica.
func (d ybTabletDefect) reason() string {
	if !d.carried {
		return d.identity + " (every node's archive omits it)"
	}

	return fmt.Sprintf(
		"%s (%d archived files are missing from every node that archived it)",
		d.identity,
		d.missing,
	)
}
