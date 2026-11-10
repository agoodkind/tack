// The scratch yugabyted failure check detects engine restarts and failed
// snapshot restorations. yugabyted restarts a crashed master or tablet server;
// the master reports a failed restoration. Either sign ends the wait at once.
// A crash loop can keep writing bytes that progress counters would otherwise
// mistake for useful work.

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
	// ybScratchLogPath identifies yugabyted's log under the --base_dir that the
	// drill supplies.
	ybScratchLogPath = "/home/yugabyte/var/logs/yugabyted.log"
	// ybScratchRestartMarker matches yugabyted log entries for restarts of a
	// stopped master or tablet server. yugabyted also logs that sentence for its
	// web server. The drill does not use the web server, and the pattern matches
	// only the two engine processes that the restore requires.
	ybScratchRestartMarker = "(master|tserver) died unexpectedly"
)

// ybRestorationFailedState matches the quoted terminal restoration state in
// the yb-admin listing. Each restoration object contains id, snapshot_id, and
// state. The quoted FAILED value does not match those JSON keys.
const ybRestorationFailedState = `"FAILED"`

// ybScratchRestartCountCommand counts engine restart lines. It returns zero
// before the log exists, when yugabyted has not written its first line or
// recorded a restart. That result counts as a reading rather than a poll that
// cannot read the failure check.
//
// TACK-499 recorded that each drill's first poll preceded the first log line.
// Three consecutive polls without a readable failure check fail the drill.
// For an existing log, the command preserves grep's exit codes: exit 1 with
// zero means that a line did not match, and exit 2 reports a read failure.
var ybScratchRestartCountCommand = []string{
	"sh",
	"-c",
	"if [ -e " + ybScratchLogPath + " ]; then grep -cE '" + ybScratchRestartMarker + "' " + ybScratchLogPath + "; else echo 0; fi",
}

// newYBScratchFailureProbe builds the failure check for one step. master
// specifies the scratch master address for restoration checks. An empty master
// disables restoration checks for a step that does not run a restoration.
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

// parseYBScratchRestarts reads one result from the count command. Exit 0
// includes a count. Exit 1 with zero means grep did not match a line. Exit 2
// means grep could not read an existing log.
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

// ybRestorationFailure returns a reason when a restoration listing contains
// the terminal FAILED state. It returns an empty string otherwise.
func ybRestorationFailure(listing string) string {
	if strings.Contains(listing, ybRestorationFailedState) {
		return "the master marked the snapshot restoration " + strings.Trim(
			ybRestorationFailedState,
			`"`,
		)
	}

	return ""
}
