// backup_restore_drill_yb_inventory.go compares one node's extracted archive
// against the inventory the export recorded for it, and reports every file the
// record names that the extraction does not hold at its recorded size. This is
// what replaced asking whether a tablet's directory looked plausible: a
// directory holding one non-empty file passed that question while missing most
// of its contents, and no question asked of the extraction alone can tell the
// difference. The inventory can, because it says what this backup captured.

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
	// ybDrillWorkDir is where the comparison writes its working lists inside
	// the scratch container. The staged artifacts are mounted read-only, so
	// they cannot be sorted in place.
	ybDrillWorkDir = "/tmp"
	// The comparison's working file names. Each is truncated by the
	// redirection that writes it, so one node's run cannot read another's
	// leftovers.
	ybInventoryHaveList = "tack-drill-extracted"
	ybInventoryWantList = "tack-drill-archived"
	ybInventoryGoneList = "tack-drill-missing"
	// ybInventoryMissingLimit bounds how many missing files one check reports.
	// The count it prints is always exact, and a check that finds more than
	// this fails the drill rather than reporting a subset: each missing file is
	// attributed to the tablet it belongs to, and files left unreported would
	// leave their tablets looking whole.
	ybInventoryMissingLimit = 4096
)

// ybInventorySizeFilter turns `wc -c` output into the inventory's own
// "path<TAB>size" lines: optional padding, the size, one space, then the "./"
// find prefixes every path with, which the inventory records without. Lines
// that do not match, which is `wc`'s per-batch total, print nothing, because
// -n prints only what the substitution matched.
const ybInventorySizeFilter = "sed -n 's/^ *\\([0-9][0-9]*\\) \\.\\/\\(.*\\)$/\\2\t\\1/p'"

// ybInventoryCheckScript builds the program that lists what one node's
// extraction holds and reports every archived file that is not there at its
// archived size. Both sides are sorted the same way and differenced with comm,
// so a corpus-sized comparison answers with one count and, on a healthy
// restore, nothing else.
//
// Only tools every POSIX shell image carries are used, and the size listing
// goes through `wc -c` rather than a `find -printf` or `stat` format, because
// those spellings differ between the GNU tools the yugabyted image ships and
// the BSD tools the tests run against, and a check that cannot run locally is a
// check nobody proves. A listing step that broke would leave the extraction
// looking empty, which fails the drill rather than passing it.
func ybInventoryCheckScript(nodeRoot, inventoryPath, workDir string) string {
	have, want, gone := shellQuote(workDir+"/"+ybInventoryHaveList),
		shellQuote(workDir+"/"+ybInventoryWantList), shellQuote(workDir+"/"+ybInventoryGoneList)
	return "set -e; cd " + shellQuote(nodeRoot) + "; " +
		"find . -type f -exec wc -c {} + | " + ybInventorySizeFilter + " | LC_ALL=C sort >" + have + "; " +
		"tail -n +2 " + shellQuote(inventoryPath) + " | LC_ALL=C sort >" + want + "; " +
		"comm -23 " + want + " " + have + " >" + gone + "; " +
		`printf 'missing %s\n' "$(wc -l <` + gone + `)"; ` +
		"head -n " + strconv.Itoa(ybInventoryMissingLimit) + " " + gone
}

// parseYBInventoryCheck reads the check's answer: the exact number of archived
// files the extraction is short of, then one line per file up to the report
// limit. A count that disagrees with the lines, or that exceeds what the check
// reports, is an error rather than a smaller list, because a partial list would
// leave the tablets it did not name looking whole.
func parseYBInventoryCheck(node, out string) ([]ybArchivedFile, error) {
	header, rest, _ := strings.Cut(strings.TrimLeft(out, "\n"), "\n")
	count, err := parseYBPlacementCount(strings.TrimSpace(header), "missing")
	if err != nil {
		return nil, fmt.Errorf("read %q as the count of files node %s's extraction is missing", header, node)
	}
	if count > ybInventoryMissingLimit {
		return nil, fmt.Errorf(
			"node %s's extraction is missing %d of the files its archive carried, more than the %d the check reports",
			node, count, ybInventoryMissingLimit)
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
		return nil, fmt.Errorf("the extraction check for node %s counted %d missing files and named %d",
			node, count, len(missing))
	}
	return missing, nil
}

// checkYBNodeExtraction runs the comparison for one node inside the scratch
// container and returns what its extraction owes the inventory.
func checkYBNodeExtraction(
	ctx context.Context,
	r *restoreDrillCtx,
	container, exportRoot string,
	inventory ybArchiveInventory,
) ([]ybArchivedFile, error) {
	logger := telemetry.L(ctx)
	script := ybInventoryCheckScript(exportRoot+"/"+inventory.Node,
		ybDrillArtifactPath(ybDrillInventoryName(inventory.Node)), ybDrillWorkDir)
	res, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", script})
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf("check node %s's extraction against its inventory: exit %d: %s: %w",
			inventory.Node, res.ExitCode, strings.TrimSpace(res.Stderr), err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	missing, err := parseYBInventoryCheck(inventory.Node, res.Stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return nil, err
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.extraction_checked",
		slog.String("node", inventory.Node),
		slog.Int("archived", len(inventory.Files)),
		slog.Int("missing", len(missing)))
	return missing, nil
}
