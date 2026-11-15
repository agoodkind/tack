// Placement audit checks that every planned tablet copy actually ran.
// Inventory validation establishes source completeness before script creation;
// this audit detects omitted script chunks, failed copies, and unreadable output.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybPlacementMissingSample limits listed tablet identities while preserving
// exact counts and reporting the number omitted from the message.
const ybPlacementMissingSample = 20

// ybPlacementAudit records attempted copies, completed copies, and missing
// tablet identities after placement scripts finish.
type ybPlacementAudit struct {
	Expected int
	Placed   int
	Missing  []string
}

// auditYBPlacement reads both placement records and verifies every imported
// tablet's completed copy.
func auditYBPlacement(
	ctx context.Context,
	r *restoreDrillCtx,
	container string,
	layout ybPlacementLayout,
	remaps []ybTabletRemap,
) error {
	logger := telemetry.L(ctx)
	res, err := containerExec(
		ctx,
		r.Cli,
		container,
		[]string{"sh", "-c", ybPlacementAuditScript(layout)},
	)
	if err != nil || res.ExitCode != 0 {
		wrapped := fmt.Errorf(
			"audit tablet placement: exit %d: %s: %w",
			res.ExitCode,
			strings.TrimSpace(res.Stderr),
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

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

	logger.InfoContext(
		ctx,
		"backup.restore_drill.yb.tablets_placed",
		slog.Int("expected", audit.Expected),
		slog.Int("placed", audit.Placed),
	)

	return nil
}

// ybPlacementVerdict compares distinct imported tablets against attempted and
// completed copies. Attempt counts detect omitted chunks; completion counts
// detect failed copies of previously verified sources.
func ybPlacementVerdict(audit ybPlacementAudit, wanted int) error {
	if audit.Expected != wanted {
		return tabletPlacementAttemptMissingError{
			Attempted: audit.Expected,
			Wanted:    wanted,
			Missing:   wanted - audit.Expected,
		}
	}

	if audit.Placed != audit.Expected {
		return tabletPlacementIncompleteError{
			Missing:    audit.Expected - audit.Placed,
			Attempted:  audit.Expected,
			Identities: sampleTabletIdentities(audit.Missing),
		}
	}

	return nil
}

// countDistinctTablets counts planned tablets using the placement records'
// identity format for comparison with attempted copies.
func countDistinctTablets(remaps []ybTabletRemap) int {
	seen := make(map[string]bool, len(remaps))
	for _, remap := range remaps {
		seen[ybTabletIdentity(remap)] = true
	}

	return len(seen)
}

// sampleTabletIdentities lists identities or refusal reasons with an omitted
// count when the sample limit applies.
func sampleTabletIdentities(identities []string) string {
	if len(identities) <= ybPlacementMissingSample {
		return strings.Join(identities, ", ")
	}

	return fmt.Sprintf(
		"%s and %d more",
		strings.Join(identities[:ybPlacementMissingSample], ", "),
		len(identities)-ybPlacementMissingSample,
	)
}

// parseYBPlacementAudit requires two counts, a missing marker, and a matching
// list of missing tablets. Count consistency detects truncated audit output.
func parseYBPlacementAudit(out string) (ybPlacementAudit, error) {
	empty := ybPlacementAudit{Expected: 0, Placed: 0, Missing: nil}
	var lines []string

	for line := range strings.SplitSeq(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	if len(lines) < 3 {
		return empty, placementAuditTruncatedError{Output: out}
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
		return empty, placementAuditCountsDisagreeError{
			Expected: expected,
			Placed:   placed,
			Missing:  len(missing),
		}
	}

	return ybPlacementAudit{Expected: expected, Placed: placed, Missing: missing}, nil
}

// parseYBPlacementCount reads one labeled count line.
func parseYBPlacementCount(line, label string) (int, error) {
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != label {
		return 0, fmt.Errorf("read %q as the placement audit's %s count", line, label)
	}

	count, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf(
			"read %q as the placement audit's %s count: it is not a whole number",
			line,
			label,
		)
	}

	if count < 0 {
		return 0, fmt.Errorf("the placement audit reported a negative %s count %d", label, count)
	}

	return count, nil
}

type placementAuditCountsDisagreeError struct {
	Expected int
	Placed   int
	Missing  int
}

func (failure placementAuditCountsDisagreeError) Error() string {
	return fmt.Sprintf(
		"the placement audit counts disagree: %d attempted minus %d placed differs from "+
			"%d listed tablets",
		failure.Expected,
		failure.Placed,
		failure.Missing,
	)
}

type placementAuditTruncatedError struct {
	Output string
}

func (failure placementAuditTruncatedError) Error() string {
	return fmt.Sprintf(
		"read %q as a placement audit: it does not include two counts and a missing marker",
		failure.Output,
	)
}

type tabletPlacementAttemptMissingError struct {
	Attempted int
	Wanted    int
	Missing   int
}

func (failure tabletPlacementAttemptMissingError) Error() string {
	return fmt.Sprintf(
		"tablet placement attempted %d of the %d tablets the import created; %d lack an attempt",
		failure.Attempted,
		failure.Wanted,
		failure.Missing,
	)
}

type tabletPlacementIncompleteError struct {
	Missing    int
	Attempted  int
	Identities string
}

func (failure tabletPlacementIncompleteError) Error() string {
	return fmt.Sprintf(
		"tablet placement is incomplete: %d of the %d attempted tablets did not record a copy: %s",
		failure.Missing,
		failure.Attempted,
		failure.Identities,
	)
}
