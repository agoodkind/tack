// The inventory format uses a header followed by one line per archived file.
// The export writes this text, and the restore reads it. The scratch
// container compares the extraction with the inventory using sort and comm.
// Plain lines avoid a separate shell JSON parser in that comparison.

package ops

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ybInventoryMarker starts the inventory header. The parser rejects a file
	// without this marker instead of accepting it as an empty inventory.
	ybInventoryMarker = "tack-yb-tablet-inventory"
	// ybInventoryHeaderFields counts the header fields for the marker, run,
	// node, and following file count.
	ybInventoryHeaderFields = 4
)

// render writes the inventory header and one path<TAB>size line per file
// in the Files order. Capture sorts Files before rendering. The restore
// compares these lines directly. The header's count lets the parser reject a
// truncated inventory rather than accept it as a smaller archive.
func (inv ybArchiveInventory) render() []byte {
	var out strings.Builder
	out.WriteString(ybInventoryMarker + " " + inv.RunID + " " + inv.Node + " " +
		strconv.Itoa(len(inv.Files)) + "\n")
	for _, file := range inv.Files {
		out.WriteString(file.Path + "\t" + strconv.FormatInt(file.Size, 10) + "\n")
	}
	return []byte(out.String())
}

// parseYBArchiveInventory reads the inventory for runID and node. It rejects
// a different run or node and a file count that differs from the header.
// Those checks prevent the restore from comparing files with another
// archive's inventory or a truncated record.
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

// parseYBInventoryHeader reads the declared file count and rejects a header
// for another run or node.
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
