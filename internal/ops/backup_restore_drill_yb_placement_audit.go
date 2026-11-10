// The placement audit checks whether every planned tablet copy completed.
// Replica selection checks export contents before script construction.
// The audit detects a script that did not run or a copy that did not record
// success. Unreadable audit output fails the drill instead of counting as a
// complete placement.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybPlacementMissingSample limits the tablet identities in one error message.
// Counts remain exact, and the message reports how many identities it omits.
// The limit prevents an error string from growing with the whole corpus.
const ybPlacementMissingSample = 20

// ybPlacementAudit records the container's counts after the placement scripts
// run: attempted tablets, successful copies, and tablets without a copy record.
type ybPlacementAudit struct {
	Expected int
	Placed   int
	Missing  []string
}

// auditYBPlacement reads both ledgers from the scratch container and fails
// unless placement accounts for every imported tablet.
func auditYBPlacement(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	layout ybPlacementLayout,
	remaps []ybTabletRemap,
) error {
	logger := telemetry.L(ctx)
	res, err := containerExec(ctx, r.Cli, container, []string{"sh", "-c", ybPlacementAuditScript(layout)})
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf("audit tablet placement: exit %d: %s: %w",
			res.ExitCode, strings.TrimSpace(res.Stderr), err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	audit, err := parseYBPlacementAudit(res.Stdout)
	if err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	if err := ybPlacementVerdict(audit, countDistinctTablets(remaps)); err != nil {
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", err.Error()))
		return err
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.tablets_placed",
		slog.Int("expected", audit.Expected), slog.Int("placed", audit.Placed))
	return nil
}

// ybPlacementVerdict requires placement to attempt every imported tablet and
// copy every attempted tablet. The expected count detects a script that did
// not run. The placed count detects a missing copy record. Each clause uses a
// source that already passed the export inventory check.
func ybPlacementVerdict(audit ybPlacementAudit, wanted int) error {
	if audit.Expected != wanted {
		return fmt.Errorf(
			"tablet placement attempted %d of the %d tablets the import created, so %d were never tried",
			audit.Expected, wanted, wanted-audit.Expected)
	}
	if audit.Placed != audit.Expected {
		return fmt.Errorf(
			"tablet placement is incomplete: %d of the %d tablets it attempted recorded no copy: %s",
			audit.Expected-audit.Placed, audit.Expected, sampleTabletIdentities(audit.Missing))
	}
	return nil
}

// countDistinctTablets counts each table and tablet identity once. The audit
// compares this count with ledger entries that use the same identity.
func countDistinctTablets(remaps []ybTabletRemap) int {
	seen := make(map[string]bool, len(remaps))
	for _, remap := range remaps {
		seen[ybTabletIdentity(remap)] = true
	}
	return len(seen)
}

// sampleTabletIdentities formats tablet identities or refusal reasons for an
// error and reports how many entries it omits.
func sampleTabletIdentities(identities []string) string {
	if len(identities) <= ybPlacementMissingSample {
		return strings.Join(identities, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(identities[:ybPlacementMissingSample], ", "),
		len(identities)-ybPlacementMissingSample)
}

// parseYBPlacementAudit reads two counts, a marker, and one line per tablet
// without a placement record. It requires the count difference to equal the
// list length and rejects inconsistent or truncated output.
func parseYBPlacementAudit(out string) (ybPlacementAudit, error) {
	empty := ybPlacementAudit{Expected: 0, Placed: 0, Missing: nil}
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) < 3 {
		return empty, fmt.Errorf("read %q as a placement audit: it has no two counts and a missing marker", out)
	}
	expected, err := parseYBPlacementCount(lines[0], "expected")
	if err != nil {
		return empty, err
	}
	placed, err := parseYBPlacementCount(lines[1], "placed")
	if err != nil {
		return empty, err
	}
	if lines[2] != "missing" {
		return empty, fmt.Errorf("read %q as the placement audit's missing marker", lines[2])
	}
	missing := lines[3:]
	if expected-placed != len(missing) {
		return empty, fmt.Errorf(
			"the placement audit does not add up: %d attempted less %d placed is not the %d tablets it named",
			expected, placed, len(missing))
	}
	return ybPlacementAudit{Expected: expected, Placed: placed, Missing: missing}, nil
}

// parseYBPlacementCount reads one labeled count from audit output.
func parseYBPlacementCount(line, label string) (int, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != label {
		return 0, fmt.Errorf("read %q as the placement audit's %s count", line, label)
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("read %q as the placement audit's %s count: it is not a whole number", line, label)
	}
	if count < 0 {
		return 0, fmt.Errorf("the placement audit reported a negative %s count %d", label, count)
	}
	return count, nil
}
