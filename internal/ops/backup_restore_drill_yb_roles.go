// backup_restore_drill_yb_roles.go applies the export's roles file to the
// restore target, before the schema, so the schema's privilege statements have
// identities to name.
//
// The drill used to infer the roles from the schema text with a regular
// expression and create each match, ignoring every error. That inferred over
// text rather than reading data: a role named outside the pattern was never
// created and nothing said so, and it created roles with no privileges, which
// is how a restored ledger came back unreadable (TACK-474). The roles file
// replaces the inference entirely; there is no fallback, because a fallback
// here would re-create the silent skip.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybRolesFileContainerPath is where the staged roles file appears inside the
// scratch container.
const ybRolesFileContainerPath = "/artifacts/" + ybSnapshotRolesObject

// ybDuplicateRoleErrorPattern matches the one error the roles file is expected
// to raise: a CREATE ROLE for a role the target already carries. 42710 is
// SQLSTATE duplicate_object, printed because the apply runs with psql's
// verbose verbosity; matching the code rather than the English sentence keeps
// the rule independent of the server's message locale.
var ybDuplicateRoleErrorPattern = regexp.MustCompile(`ERROR:\s+42710:\s+role "([^"]+)" already exists`)

// ybRolesApplyReport is what one roles-file application produced: the roles
// the target already carried, and every error line that was not that.
type ybRolesApplyReport struct {
	// AlreadyPresent names the roles whose CREATE was refused because the
	// target already has them.
	AlreadyPresent []string
	// Unexpected holds every other error line, verbatim.
	Unexpected []string
}

// classifyYBRolesApply reads the apply's stderr and sorts every error line into
// the one tolerated class or the rest.
//
// Exactly one class is tolerated: a role that already exists. The engine
// creates its own roles at initdb (postgres, yugabyte, yb_db_admin,
// yb_extension, yb_fdw) and the target's bootstrap user exists before any
// restore begins, while the roles file describes every role the source cluster
// held, including those. Nothing is lost by passing over the refused CREATE:
// the dump emits CREATE ROLE followed immediately by ALTER ROLE with the
// role's attributes, and that ALTER still runs, so the role ends up as the
// backup described it either way.
//
// Every other error fails the restore. The classification is deny-by-default:
// an error line whose SQLSTATE is absent or unrecognized counts as unexpected,
// so a failure this rule has never seen stops the drill instead of passing
// through it.
func classifyYBRolesApply(stderr string) ybRolesApplyReport {
	report := ybRolesApplyReport{AlreadyPresent: nil, Unexpected: nil}
	for line := range strings.SplitSeq(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "ERROR:") {
			continue
		}
		match := ybDuplicateRoleErrorPattern.FindStringSubmatch(trimmed)
		if match == nil {
			report.Unexpected = append(report.Unexpected, trimmed)
			continue
		}
		report.AlreadyPresent = append(report.AlreadyPresent, match[1])
	}
	return report
}

// applyYBDrillRoles applies the staged roles file to the scratch database.
//
// The apply deliberately does not run under ON_ERROR_STOP. The roles file
// describes the source cluster's whole role set, which always includes roles
// the target already has, so stopping at the first of them would abort the
// file part-applied and fail every restore. Reading the errors back and
// refusing any that is not that one class is what keeps this from being a
// blanket "ignore failures": a role the target could not be given, for any
// other reason, fails the drill here rather than surfacing later as a ledger
// nobody can read.
func applyYBDrillRoles(ctx context.Context, r *restoreDrillCtx, containerName, database string) error {
	logger := telemetry.L(ctx)
	cmd := []string{
		"ysqlsh", "-h", containerName, "-p", drillLedgerPort,
		"-U", database, "-d", database,
		"-v", "VERBOSITY=verbose", "-q", "-f", ybRolesFileContainerPath,
	}
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, containerName, cmd,
		[]string{"PGPASSWORD=" + r.YBPass}, devNull{})
	if err != nil {
		wrapped := fmt.Errorf("apply the export's roles file: %w", err)
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if exitCode != 0 {
		wrapped := fmt.Errorf("apply the export's roles file: ysqlsh exited %d: %s",
			exitCode, strings.TrimSpace(stderr))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	report := classifyYBRolesApply(stderr)
	if len(report.Unexpected) > 0 {
		wrapped := fmt.Errorf("the export's roles file did not apply cleanly: %s",
			strings.Join(report.Unexpected, "; "))
		logger.ErrorContext(ctx, "backup.restore_drill.yb.failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.yb.roles_applied",
		slog.Any("already_present", report.AlreadyPresent))
	return nil
}
