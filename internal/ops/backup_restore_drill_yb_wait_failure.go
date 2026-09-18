// backup_restore_drill_yb_wait_failure.go reads the two signs that a scratch
// yugabyted step can never succeed: yugabyted restarting a master or tablet
// server that crashed, and the master marking a snapshot restoration failed.
// Each ends the wait at once, because a crash loop keeps writing bytes that
// the progress counters would otherwise read as movement.

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
	// ybScratchLogPath is yugabyted's own log under the --base_dir the drill
	// starts it with.
	ybScratchLogPath = "/home/yugabyte/var/logs/yugabyted.log"
	// ybScratchRestartMarker matches the line yugabyted logs when it restarts
	// a master or tablet server that stopped running. yugabyted logs the same
	// sentence for its own web server, which the drill never uses, so the
	// pattern names the two processes the restore depends on.
	ybScratchRestartMarker = "(master|tserver) died unexpectedly"
)

// ybRestorationFailedState is the state a restoration never leaves, quoted as
// the listing prints it. yb-admin renders the restoration's state enum into a
// three-field JSON object (id, snapshot_id, state), so this cannot match a
// field name.
const ybRestorationFailedState = `"FAILED"`

// ybScratchRestartCountCommand counts the restart lines, and answers 0 when
// the log does not exist yet: a scratch yugabyted that has not written its
// first line has restarted nothing, which is a reading rather than a blind
// poll. Every drill's first poll runs before that first line (TACK-499), and
// three blind polls in a row fail the drill. grep's own exit codes are kept
// for a log that exists: 1 with "0" when nothing matched, 2 when it cannot be
// read.
var ybScratchRestartCountCommand = []string{
	"sh", "-c",
	"if [ -e " + ybScratchLogPath + " ]; then grep -cE '" + ybScratchRestartMarker + "' " + ybScratchLogPath + "; else echo 0; fi",
}

// newYBScratchFailureProbe builds the failure check for one step. master is
// the scratch master address whose restorations are checked, or "" for a step
// that runs no restoration.
func newYBScratchFailureProbe(r *restoreDrillCtx, container, master string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		res, err := containerExec(ctx, r.Cli, container, ybScratchRestartCountCommand)
		if err != nil {
			wrapped := fmt.Errorf("count yugabyted restarts: %w", err)
			telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.restarts_unreadable", slog.String("err", wrapped.Error()))
			return "", wrapped
		}
		restarts, err := parseYBScratchRestarts(ctx, res.ExitCode, res.Stdout)
		if err != nil {
			return "", err
		}
		if restarts > 0 {
			return fmt.Sprintf("yugabyted restarted a crashed master or tablet server %d time(s)", restarts), nil
		}
		if master == "" {
			return "", nil
		}
		listing, err := containerExec(ctx, r.Cli, container, []string{ybAdminBinary, "--master_addresses", master, "list_snapshot_restorations"})
		if err != nil {
			wrapped := fmt.Errorf("list snapshot restorations: %w", err)
			telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.restorations_unreadable", slog.String("err", wrapped.Error()))
			return "", wrapped
		}
		if listing.ExitCode != 0 {
			wrapped := fmt.Errorf("list snapshot restorations exited %d: %s", listing.ExitCode, strings.TrimSpace(listing.Stderr))
			telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.restorations_unreadable", slog.String("err", wrapped.Error()))
			return "", wrapped
		}
		return ybRestorationFailure(listing.Stdout), nil
	}
}

// parseYBScratchRestarts reads the count from one run of the count command:
// 0 with a count, 1 with "0" when grep matched nothing, 2 when a log that
// exists could not be read.
func parseYBScratchRestarts(ctx context.Context, exitCode int, stdout string) (int64, error) {
	fields := strings.Fields(stdout)
	if exitCode > 1 || len(fields) == 0 {
		err := fmt.Errorf("count yugabyted restarts in %s: grep exited %d with %q", ybScratchLogPath, exitCode, strings.TrimSpace(stdout))
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.restarts_unreadable", slog.String("err", err.Error()))
		return 0, err
	}
	restarts, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		wrapped := fmt.Errorf("parse yugabyted restart count %q: %w", fields[0], err)
		telemetry.L(ctx).WarnContext(ctx, "backup.restore_drill.yb.restarts_unreadable", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	return restarts, nil
}

// ybRestorationFailure returns why a restoration listing shows a restoration
// that will never reach RESTORED, or "" when none does.
func ybRestorationFailure(listing string) string {
	if strings.Contains(listing, ybRestorationFailedState) {
		return "the master marked the snapshot restoration " + strings.Trim(ybRestorationFailedState, `"`)
	}
	return ""
}
