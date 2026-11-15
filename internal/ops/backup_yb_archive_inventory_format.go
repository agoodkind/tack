// Tablet inventories use a header and one path-and-size line per file.
// Restore containers compare this format directly with sort and comm without
// requiring a JSON parser.

package ops

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ybInventoryMarker identifies an inventory header and prevents accepting
	// unrelated files as empty inventories.
	ybInventoryMarker = "tack-yb-tablet-inventory"
	// ybInventoryHeaderFields counts the marker, run, node, and file count.
	ybInventoryHeaderFields = 4
)

// render writes the header and sorted "path<TAB>size" lines for restore
// comparison. The header's file count detects truncated inventories rather
// than accepting an incomplete list as the full archive.
func (inv ybArchiveInventory) render() []byte {
	var out strings.Builder
	out.WriteString(
		ybInventoryMarker + " " + inv.RunID + " " + inv.Node + " " +
			strconv.Itoa(len(inv.Files)) + "\n",
	)

	for _, file := range inv.Files {
		out.WriteString(file.Path + "\t" + strconv.FormatInt(file.Size, 10) + "\n")
	}

	return []byte(out.String())
}

// parseYBArchiveInventory verifies the expected run, node, file count, and file
// records. A truncated inventory or one from another source cannot establish
// that the extraction contains the expected files.
func parseYBArchiveInventory(runID, node string, body []byte) (ybArchiveInventory, error) {
	empty := ybArchiveInventory{RunID: "", Node: "", Files: nil}
	header, rest, found := strings.Cut(string(body), "\n")
	if !found {
		return empty, tabletInventoryHeaderMissingError{Header: header, Node: node}
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
		return empty, tabletInventoryCountMismatchError{
			Node:     node,
			Declared: declared,
			Actual:   len(files),
		}
	}

	return ybArchiveInventory{RunID: runID, Node: node, Files: files}, nil
}

// parseYBInventoryHeader verifies the run and node and returns the file count.
func parseYBInventoryHeader(header, runID, node string) (int, error) {
	fields := strings.Fields(header)
	if len(fields) != ybInventoryHeaderFields || fields[0] != ybInventoryMarker {
		return 0, fmt.Errorf("read %q as a tablet inventory header", header)
	}

	if fields[1] != runID || fields[2] != node {
		return 0, tabletInventorySourceMismatchError{
			Run:        runID,
			Node:       node,
			ActualRun:  fields[1],
			ActualNode: fields[2],
		}
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

type tabletInventoryCountMismatchError struct {
	Node     string
	Declared int
	Actual   int
}

func (failure tabletInventoryCountMismatchError) Error() string {
	return fmt.Sprintf(
		"the tablet inventory for node %s declares %d files and contains %d",
		failure.Node,
		failure.Declared,
		failure.Actual,
	)
}

type tabletInventoryHeaderMissingError struct {
	Header string
	Node   string
}

func (failure tabletInventoryHeaderMissingError) Error() string {
	return fmt.Sprintf(
		"read %q as the tablet inventory for node %s: it does not include a header line",
		failure.Header,
		failure.Node,
	)
}

type tabletInventorySourceMismatchError struct {
	Run        string
	Node       string
	ActualRun  string
	ActualNode string
}

func (failure tabletInventorySourceMismatchError) Error() string {
	return fmt.Sprintf(
		"the tablet inventory read for run %s node %s belongs to run %s node %s",
		failure.Run,
		failure.Node,
		failure.ActualRun,
		failure.ActualNode,
	)
}
