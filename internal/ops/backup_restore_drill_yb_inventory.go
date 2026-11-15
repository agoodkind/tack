// Extraction checks compare each node's files and sizes with its archived
// inventory. Directory presence or a single nonempty file cannot establish
// that a tablet's complete backup exists.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybDrillWorkDir stores comparison lists outside read-only artifact mounts.
	ybDrillWorkDir = "/tmp"
	// Each comparison truncates these files before reading them to prevent
	// reuse of another node's results.
	ybInventorySizeList = "tack-drill-sizes"
	ybInventoryHaveList = "tack-drill-extracted"
	ybInventoryWantList = "tack-drill-archived"
	ybInventoryGoneList = "tack-drill-missing"
	// ybInventoryMissingLimit bounds reported files. Counts remain exact;
	// exceeding the limit fails rather than returning a partial list that
	// could incorrectly indicate complete tablets.
	ybInventoryMissingLimit = 4096
)

// ybInventorySizeFilter converts padded wc counts and ./-prefixed paths to
// "path<TAB>size" records. Nonmatching per-batch totals do not produce output.
const ybInventorySizeFilter = "sed -n 's/^ *\\([0-9][0-9]*\\) \\.\\/\\(.*\\)$/\\2\t\\1/p'"

// ybInventoryCheckScript compares sorted extracted and archived path-size
// lists with comm. It reports an exact count and bounded missing-file list.
// Portable wc -c avoids incompatible GNU and BSD find/stat formats.
//
// Separate files replace pipelines to let POSIX set -e detect every failed
// read. A pipeline could hide an unreadable inventory behind sort's success
// and incorrectly accept an empty expected list. dash does not support
// pipefail. The count assignment also exposes substitution failure that a
// substitution inside printf arguments could hide.
func ybInventoryCheckScript(nodeRoot, inventoryPath, workDir string) string {
	sizes, have := shellQuote(workDir+"/"+ybInventorySizeList), shellQuote(
		workDir+"/"+ybInventoryHaveList,
	)
	want, gone := shellQuote(workDir+"/"+ybInventoryWantList), shellQuote(
		workDir+"/"+ybInventoryGoneList,
	)

	return "set -e; cd " + shellQuote(nodeRoot) + "; " +
		"find . -type f -exec wc -c {} + >" + sizes + "; " +
		ybInventorySizeFilter + " " + sizes + " >" + have + "; " +
		"LC_ALL=C sort -o " + have + " " + have + "; " +
		"tail -n +2 " + shellQuote(inventoryPath) + " >" + want + "; " +
		"LC_ALL=C sort -o " + want + " " + want + "; " +
		"comm -23 " + want + " " + have + " >" + gone + "; " +
		"count=$(wc -l <" + gone + "); " +
		`printf 'missing %s\n' "$count"; ` +
		"head -n " + strconv.Itoa(ybInventoryMissingLimit) + " " + gone
}

// parseYBInventoryCheck requires a complete missing-file list. Counts above
// the report limit or inconsistent with the list fail rather than allowing
// unreported files to make their tablets appear complete.
func parseYBInventoryCheck(node, out string) ([]ybArchivedFile, error) {
	header, rest, _ := strings.Cut(strings.TrimLeft(out, "\n"), "\n")
	count, err := parseYBPlacementCount(strings.TrimSpace(header), "missing")
	if err != nil {
		return nil, fmt.Errorf(
			"read %q as the count of files node %s's extraction is missing",
			header,
			node,
		)
	}

	if count > ybInventoryMissingLimit {
		return nil, extractionMissingFilesOverflowError{
			Node:  node,
			Count: count,
			Limit: ybInventoryMissingLimit,
		}
	}

	var missing []ybArchivedFile

	for line := range strings.SplitSeq(strings.TrimSuffix(rest, "\n"), "\n") {
		if line == "" {
			continue
		}

		file, fileErr := parseYBArchivedFile(line, node)
		if fileErr != nil {
			return nil, fileErr
		}

		missing = append(missing, file)
	}

	if len(missing) != count {
		return nil, extractionCountMismatchError{Node: node, Count: count, Listed: len(missing)}
	}

	return missing, nil
}

// checkYBNodeExtraction compares one node inside the scratch container and
// returns the archived files missing from its extraction.
func checkYBNodeExtraction(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	exportRoot string,
	inventory ybArchiveInventory,
) ([]ybArchivedFile, error) {
	logger := telemetry.L(ctx)
	script := ybInventoryCheckScript(
		exportRoot+"/"+inventory.Node,
		ybDrillArtifactPath(ybDrillInventoryName(inventory.Node)),
		ybDrillWorkDir,
	)
	res, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", script})
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf(
			"check node %s's extraction against its inventory: exit %d: %s: %w",
			inventory.Node,
			res.ExitCode,
			strings.TrimSpace(res.Stderr),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	missing, err := parseYBInventoryCheck(inventory.Node, res.Stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return nil, err
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.yb.extraction_checked",
		slog.String("node", inventory.Node),
		slog.Int("archived", len(inventory.Files)),
		slog.Int("missing", len(missing)),
	)

	return missing, nil
}

type extractionCountMismatchError struct {
	Node   string
	Count  int
	Listed int
}

func (failure extractionCountMismatchError) Error() string {
	return fmt.Sprintf(
		"the extraction check for node %s counted %d missing files and listed %d",
		failure.Node,
		failure.Count,
		failure.Listed,
	)
}

type extractionMissingFilesOverflowError struct {
	Node  string
	Count int
	Limit int
}

func (failure extractionMissingFilesOverflowError) Error() string {
	return fmt.Sprintf(
		"node %s's extraction is missing %d archived files, more than the %d the check reports",
		failure.Node,
		failure.Count,
		failure.Limit,
	)
}
