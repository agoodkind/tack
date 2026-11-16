// Scratch failure probes detect engine crash restarts and failed snapshot
// restorations. Both stop monitoring immediately; crash-loop writes must not
// count as successful engine progress.

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
	// ybScratchLogPath specifies yugabyted's log under the scratch base directory.
	ybScratchLogPath = "/home/yugabyte/var/logs/yugabyted.log"
	// ybScratchRestartMarker selects master and tablet-server restart messages,
	// excluding the unused web server's matching message format.
	ybScratchRestartMarker = "(master|tserver) died unexpectedly"
)

// ybRestorationFailedState matches the terminal enum value in yb-admin's
// id, snapshot_id, state JSON output rather than a field name.
const ybRestorationFailedState = `"FAILED"`

// ybScratchRestartCountCommand returns zero before the log exists (TACK-499).
// Later reads preserve grep status: exit 1 with zero matches indicates a valid
// zero count; exit 2 indicates a failed read. Initial absent logs must not
// accumulate unanswered polls that would incorrectly fail startup.
var ybScratchRestartCountCommand = []string{
	"sh",
	"-c",
	"if [ -e " + ybScratchLogPath + " ]; then grep -cE '" + ybScratchRestartMarker + "' " + ybScratchLogPath + "; else echo 0; fi",
}

// newYBScratchFailureProbe checks restart counts and, when master is nonempty,
// that master's snapshot restoration states.
func newYBScratchFailureProbe(
	r *restoreDrillCtx,
	container string,
	master string,
) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		res, err := containerExec(ctx, r.Cli, container, ybScratchRestartCountCommand)
		if err != nil {
			wrapped := fmt.Errorf("count yugabyted restarts: %w", err)
			telemetry.L(ctx).WarnContext(
				ctx,
				"backup.restore_drill.yb.restarts_unreadable",
				slog.String("err", wrapped.Error()),
			)

			return "", wrapped
		}

		restarts, err := parseYBScratchRestarts(ctx, res.ExitCode, res.Stdout)
		if err != nil {
			return "", err
		}

		if restarts > 0 {
			return fmt.Sprintf(
				"yugabyted restarted a crashed master or tablet server %d time(s)",
				restarts,
			), nil
		}

		if master == "" {
			return "", nil
		}

		listing, err := containerExec(
			ctx,
			r.Cli,
			container,
			[]string{ybAdminBinary, "--master_addresses", master, "list_snapshot_restorations"},
		)
		if err != nil {
			wrapped := fmt.Errorf("list snapshot restorations: %w", err)
			telemetry.L(ctx).WarnContext(
				ctx,
				"backup.restore_drill.yb.restorations_unreadable",
				slog.String("err", wrapped.Error()),
			)

			return "", wrapped
		}

		if listing.ExitCode != 0 {
			wrapped := fmt.Errorf(
				"list snapshot restorations exited %d: %s",
				listing.ExitCode,
				strings.TrimSpace(listing.Stderr),
			)
			telemetry.L(ctx).WarnContext(
				ctx,
				"backup.restore_drill.yb.restorations_unreadable",
				slog.String("err", wrapped.Error()),
			)

			return "", wrapped
		}

		return ybRestorationFailure(listing.Stdout), nil
	}
}

// parseYBScratchRestarts accepts a count with exit 0 or grep's zero-match
// exit 1. An empty result or a higher exit status fails the read.
func parseYBScratchRestarts(ctx context.Context, exitCode int, stdout string) (int64, error) {
	fields := strings.Fields(stdout)
	if exitCode > 1 || len(fields) == 0 {
		err := fmt.Errorf(
			"count yugabyted restarts in %s: grep exited %d with %q",
			ybScratchLogPath,
			exitCode,
			strings.TrimSpace(stdout),
		)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.restarts_unreadable",
			slog.String("err", err.Error()),
		)

		return 0, err
	}

	restarts, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse yugabyted restart count %q: %w", fields[0], err)
		telemetry.L(ctx).WarnContext(
			ctx,
			"backup.restore_drill.yb.restarts_unreadable",
			slog.String("err", wrapped.Error()),
		)

		return 0, wrapped
	}

	return restarts, nil
}

// ybRestorationFailure reports a terminal restoration failure or an empty
// string when the listing does not indicate failure.
func ybRestorationFailure(listing string) string {
	if strings.Contains(listing, ybRestorationFailedState) {
		return "the master marked the snapshot restoration " + strings.Trim(
			ybRestorationFailedState,
			`"`,
		)
	}

	return ""
}
