// backup_yb_archive_inventory_format.go is the inventory's wire form: how the
// export writes what an archive carries, and how the restore reads it back. It
// is a header line and one line per file rather than JSON, because the scratch
// container compares its extraction against the same bytes with sort and comm,
// and a shell that had to parse JSON first would be the weaker half of the
// check.

package ops

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ybInventoryMarker opens the inventory's header line, so a file that is
	// not an inventory is refused rather than read as one carrying no files.
	ybInventoryMarker = "tack-yb-tablet-inventory"
	// ybInventoryHeaderFields is the header's field count: the marker, the run,
	// the node, and how many file lines follow.
	ybInventoryHeaderFields = 4
)

// render writes the inventory as its header line followed by one
// "path<TAB>size" line per file, sorted, which is the form the restore's
// comparison consumes directly. The header carries the file count so an
// inventory that arrives short is refused rather than read as a smaller
// archive, which would be a weaker check that still passed.
func (inv ybArchiveInventory) render() []byte {
	var out strings.Builder
	out.WriteString(ybInventoryMarker + " " + inv.RunID + " " + inv.Node + " " +
		strconv.Itoa(len(inv.Files)) + "\n")
	for _, file := range inv.Files {
		out.WriteString(file.Path + "\t" + strconv.FormatInt(file.Size, 10) + "\n")
	}
	return []byte(out.String())
}

// parseYBArchiveInventory reads an inventory that must have been recorded for
// runID and node. It is strict throughout: an inventory expects exactly the
// files it lists, so one that arrives short, or that belongs to another run or
// node, expects the wrong set, and a restore checked against the wrong set
// passes on the wrong set.
func parseYBArchiveInventory(runID, node string, body []byte) (ybArchiveInventory, error) {
	empty := ybArchiveInventory{RunID: "", Node: "", Files: nil}
	header, rest, found := strings.Cut(string(body), "\n")
	if !found {
		return empty, fmt.Errorf("read %q as the tablet inventory for node %s: it has no header line", header, node)
	}
	declared, err := parseYBInventoryHeader(header, runID, node)
	if err != nil {
		return empty, err
	}
	var files []ybArchivedFile
	for line := range strings.SplitSeq(strings.TrimSuffix(rest, "\n"), "\n") {
		if line == "" {
			continue
		}
		file, fileErr := parseYBArchivedFile(line, node)
		if fileErr != nil {
			return empty, fileErr
		}
		files = append(files, file)
	}
	if len(files) != declared {
		return empty, fmt.Errorf("the tablet inventory for node %s declares %d files and carries %d",
			node, declared, len(files))
	}
	return ybArchiveInventory{RunID: runID, Node: node, Files: files}, nil
}

// parseYBInventoryHeader reads the header line and returns the file count it
// declares, refusing an inventory recorded for another run or node.
func parseYBInventoryHeader(header, runID, node string) (int, error) {
	fields := strings.Fields(header)
	if len(fields) != ybInventoryHeaderFields || fields[0] != ybInventoryMarker {
		return 0, fmt.Errorf("read %q as a tablet inventory header", header)
	}
	if fields[1] != runID || fields[2] != node {
		return 0, fmt.Errorf("the tablet inventory read for run %s node %s was recorded for run %s node %s",
			runID, node, fields[1], fields[2])
	}
	declared, err := strconv.Atoi(fields[3])
	if err != nil || declared < 0 {
		return 0, fmt.Errorf("read %q as the tablet inventory's file count", fields[3])
	}
	return declared, nil
}

// parseYBArchivedFile reads one "path<TAB>size" line.
func parseYBArchivedFile(line, node string) (ybArchivedFile, error) {
	empty := ybArchivedFile{Path: "", Size: 0}
	path, sizeText, found := strings.Cut(line, "\t")
	if !found || path == "" {
		return empty, fmt.Errorf("read %q as a tablet inventory line for node %s", line, node)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return empty, fmt.Errorf("read %q as the archived size of %s", sizeText, path)
	}
	return ybArchivedFile{Path: path, Size: size}, nil
}
