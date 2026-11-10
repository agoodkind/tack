// backup_restore_drill_yb_placement_audit.go reads what the tablet placement
// covered and decides whether every copy it was built to make actually ran.
// What the export carried is settled before the scripts exist, so this is the
// half that catches the scripts themselves falling short: a chunk that never
// ran, or a copy that recorded nothing. Output it cannot read fails the drill
// rather than counting as nothing missing.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybPlacementMissingSample bounds how many missing tablet names one error
// message carries. The counts it reports are always exact and the message
// says how many names it left out, so nothing is dropped without saying so;
// what the bound prevents is a corpus-sized error string.
const ybPlacementMissingSample = 20

// ybPlacementAudit is what the container counted once every placement script
// had run: how many tablets the placement attempted, how many it copied, and
// which ones recorded no copy.
type ybPlacementAudit struct {
	Expected int
	Placed   int
	Missing  []string
}

// auditYBPlacement reads both ledgers out of the scratch container and fails
// unless the placement covered every tablet the import created.
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

// ybPlacementVerdict fails unless the placement attempted every tablet the
// import created and copied every one it attempted. Both halves matter: the
// attempted count catches a chunk that never ran, and the placed count catches
// a copy that did not, since a clause is built only for a tablet whose source
// was already verified against the export's own record of it.
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

// countDistinctTablets counts the tablets the placement scripts were built
// from, under the same identity the ledgers record, so the audit's attempted
// count is compared against a like number.
func countDistinctTablets(remaps []ybTabletRemap) int {
	seen := make(map[string]bool, len(remaps))
	for _, remap := range remaps {
		seen[ybTabletIdentity(remap)] = true
	}
	return len(seen)
}

// sampleTabletIdentities renders tablet names, or the reasons tablets were
// refused, for an error message, naming how many it did not list.
func sampleTabletIdentities(identities []string) string {
	if len(identities) <= ybPlacementMissingSample {
		return strings.Join(identities, ", ")
	}
	return fmt.Sprintf("%s and %d more",
		strings.Join(identities[:ybPlacementMissingSample], ", "),
		len(identities)-ybPlacementMissingSample)
}

// parseYBPlacementAudit reads the audit script's output, which is two counts,
// a marker, and one line per tablet the export did not carry. It is strict on
// purpose, and the two counts have to agree with the list, which is what
// catches an audit read that came back truncated.
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

// parseYBPlacementCount reads one labelled count line of the audit output.
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
