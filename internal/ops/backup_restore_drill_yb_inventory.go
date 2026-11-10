// The extraction check compares a node's restored files with the paths and
// sizes in its export inventory. It reports every recorded file that is absent
// or has a different size. An earlier nonempty-directory check could accept
// one remaining file while most contents were missing. The recorded inventory
// provides the backup baseline that extraction contents alone cannot supply.

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
	// ybDrillWorkDir selects the scratch-container directory for working lists.
	// The staged artifacts mount read-only and cannot support in-place sorting.
	ybDrillWorkDir = "/tmp"
	// The comparison truncates each working list when writing it. A node check
	// must not reuse an earlier node's list contents.
	ybInventorySizeList = "tack-drill-sizes"
	ybInventoryHaveList = "tack-drill-extracted"
	ybInventoryWantList = "tack-drill-archived"
	ybInventoryGoneList = "tack-drill-missing"
	// ybInventoryMissingLimit caps the list of missing files while the check
	// always reports the exact count. Exceeding the cap fails the drill. Every
	// missing file requires tablet attribution; accepting an incomplete list would
	// leave some tablets incorrectly classified as complete.
	ybInventoryMissingLimit = 4096
)

// ybInventorySizeFilter converts wc -c output to path<TAB>size records. It
// matches optional padding, a size, one space, and the ./ prefix that find
// adds. The inventory omits that prefix. The sed -n substitution prints only
// matched lines and excludes wc's per-batch total.
const ybInventorySizeFilter = "sed -n 's/^ *\\([0-9][0-9]*\\) \\.\\/\\(.*\\)$/\\2\t\\1/p'"

// ybInventoryCheckScript compares extracted paths and sizes with one node's
// inventory. It sorts both lists identically and uses comm to identify missing
// or changed-size files. The output starts with a count; a complete extraction
// does not print file rows.
//
// The script uses tools from POSIX shell images. wc -c supplies file sizes
// instead of find -printf or stat formats that differ between the GNU tools
// in the yugabyted image and the BSD tools in local tests. Local execution
// permits tests to check the same program.
//
// Each command writes a file that the next command reads instead of using
// pipelines. With set -e, a pipeline's final status could hide a failed read:
// sort could accept an empty input and the check could incorrectly
// report a complete extraction. Separate commands make that failed read stop
// the script under POSIX sh. The tests also use dash, which rejects
// set -o pipefail.
//
// The count uses an assignment. A failed command substitution inside printf
// arguments would not make printf fail.
func ybInventoryCheckScript(nodeRoot, inventoryPath, workDir string) string {
	sizes, have := shellQuote(workDir+"/"+ybInventorySizeList), shellQuote(workDir+"/"+ybInventoryHaveList)
	want, gone := shellQuote(workDir+"/"+ybInventoryWantList), shellQuote(workDir+"/"+ybInventoryGoneList)
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

// parseYBInventoryCheck reads the exact missing-file count followed by file
// records up to the report limit. A count above the limit or a disagreement
// between the count and records returns an error. A partial list cannot
// establish which tablets have missing files.
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

// checkYBNodeExtraction runs the comparison inside the scratch container and
// returns the archived files that the extraction lacks at their recorded size.
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
