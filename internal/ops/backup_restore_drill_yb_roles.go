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
//
// Reading the apply's errors back means reading text a database renders, and a
// database renders that text in whatever message locale it is set to. So the
// apply pins the rendering rather than trying to understand every rendering:
// the server's lc_messages and the client's own message locale are both fixed
// on the connection, and what the classification keys off is the SQLSTATE,
// which is a SQL-standard identifier no locale rewrites. The severity token is
// no longer read at all.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybDuplicateObjectSQLState is SQLSTATE 42710, duplicate_object, which a
// CREATE ROLE for a role the target already carries raises.
const ybDuplicateObjectSQLState = "42710"

// ybDiagnosticPattern matches the rendered shape psql gives every server
// diagnostic under VERBOSITY=verbose: a severity token, the five-character
// SQLSTATE, then the message. The severity token is matched only to reach the
// code behind it and is never read, because the server localizes it; the code
// and the message are captured. Verified live against
// yugabytedb/yugabyte:2025.2.3.0-b149 on 2026-08-30, which renders
// "ERROR:  42710: role ..." under lc_messages=C and
// "FEHLER:  42710: Rolle ..." under lc_messages=de_DE.utf8, and puts the same
// code on a notice ("NOTICE:  00000: ...", "HINWEIS:  00000: ...").
var ybDiagnosticPattern = regexp.MustCompile(`(?:^|\s)[^\s:]+:\s+([0-9A-Z]{5}):\s+(.*)$`)

// ybDuplicateRoleMessagePattern matches the message a duplicate CREATE ROLE
// raises, in the C locale the apply pins. It reads the message only to tell
// which object the duplicate was: SQLSTATE 42710 covers duplicate objects of
// every kind, and only a duplicate role is tolerable in a roles file.
var ybDuplicateRoleMessagePattern = regexp.MustCompile(`^role "([^"]+)" already exists$`)

// ybRolesApplyReport is what one roles-file application produced: the roles
// the target already carried, and every error line that was not that.
type ybRolesApplyReport struct {
	// AlreadyPresent names the roles whose CREATE was refused because the
	// target already has them.
	AlreadyPresent []string
	// Unexpected holds every other error line, verbatim.
	Unexpected []string
}

// ybSQLStateClass is the two-character class a SQLSTATE opens with, which is
// what says whether a diagnostic reports a failure.
type ybSQLStateClass string

const (
	// ybSQLStateClassSuccess is class 00, successful completion, which is what
	// a notice carries.
	ybSQLStateClassSuccess ybSQLStateClass = "00"
	// ybSQLStateClassWarning is class 01, a warning rather than a statement
	// that did not run.
	ybSQLStateClassWarning ybSQLStateClass = "01"
	// ybSQLStateClassNoData is class 02, an empty result rather than a
	// failure.
	ybSQLStateClassNoData ybSQLStateClass = "02"
)

// ybSQLStateIsFailure reports whether a SQLSTATE names a failure. The SQL
// standard reserves three classes for outcomes that are not exceptions and
// leaves every other class an exception. Reading the class rather than the
// severity word is what makes the failure-or-not decision independent of the
// server's message locale.
func ybSQLStateIsFailure(sqlState string) bool {
	switch ybSQLStateClass(sqlState[:2]) {
	case ybSQLStateClassSuccess, ybSQLStateClassWarning, ybSQLStateClassNoData:
		return false
	default:
		return true
	}
}

// ybDuplicateRoleName returns the role a diagnostic says already exists, or ""
// when the diagnostic is not that. Both halves must agree: the SQLSTATE says a
// duplicate object, and the message says the object was a role.
func ybDuplicateRoleName(sqlState, message string) string {
	if sqlState != ybDuplicateObjectSQLState {
		return ""
	}
	match := ybDuplicateRoleMessagePattern.FindStringSubmatch(message)
	if match == nil {
		return ""
	}
	return match[1]
}

// classifyYBRolesApply reads the apply's stderr and sorts every diagnostic into
// the one tolerated class or the rest. rolesFilePath is the file the apply was
// pointed at, which is how a diagnostic about that file is recognized without
// reading any word a locale could change.
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
// Every other diagnostic fails the restore. The classification is
// deny-by-default in both directions: a diagnostic whose SQLSTATE is a failure
// but whose message is not the tolerated one counts as unexpected, and so does
// a line psql raised about the roles file that carries no SQLSTATE at all,
// which means the apply did not run under the verbose verbosity this reads. A
// rendering this rule cannot account for therefore stops the drill instead of
// passing through it unread.
func classifyYBRolesApply(stderr, rolesFilePath string) ybRolesApplyReport {
	report := ybRolesApplyReport{AlreadyPresent: nil, Unexpected: nil}
	for line := range strings.SplitSeq(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		match := ybDiagnosticPattern.FindStringSubmatch(trimmed)
		if match == nil {
			if strings.Contains(trimmed, rolesFilePath+":") {
				report.Unexpected = append(report.Unexpected, trimmed)
			}
			continue
		}
		sqlState, message := match[1], match[2]
		if !ybSQLStateIsFailure(sqlState) {
			continue
		}
		role := ybDuplicateRoleName(sqlState, message)
		if role == "" {
			report.Unexpected = append(report.Unexpected, trimmed)
			continue
		}
		report.AlreadyPresent = append(report.AlreadyPresent, role)
	}
	return report
}

// ybRolesApplyEnv is the environment the roles apply runs under. Past the
// password it is one pin, applied to both halves of what the apply prints:
// PGOPTIONS fixes the server's lc_messages, which is what renders the severity
// token and the message text, and the client locale variables fix the labels
// libpq renders itself (LOCATION, DETAIL). An empty LC_ALL is how an LC_ALL
// inherited from the image is taken out of the way without disturbing
// collation or character type, which belong to the restored data rather than
// to its diagnostics.
//
// lc_messages is a superuser-only setting, and the scratch cluster's bootstrap
// user is a superuser (verified on yugabytedb/yugabyte:2025.2.3.0-b149), so
// this is accepted at connection time. Were that ever untrue the connection
// would be refused outright, which fails the drill loudly rather than quietly
// handing the classification a rendering it cannot read.
func ybRolesApplyEnv(password string) []string {
	return []string{
		"PGPASSWORD=" + password,
		"PGOPTIONS=-c lc_messages=C",
		"LC_ALL=",
		"LC_MESSAGES=C",
	}
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
// nobody can read. VERBOSITY=verbose is load-bearing for that reading, because
// it is what puts the SQLSTATE on every diagnostic.
func applyYBDrillRoles(ctx context.Context, r *restoreDrillCtx, containerName, database string) error {
	logger := telemetry.L(ctx)
	rolesFilePath := ybDrillArtifactPath(ybSnapshotRolesObject)
	cmd := []string{
		"ysqlsh", "-h", containerName, "-p", drillLedgerPort,
		"-U", database, "-d", database,
		"-v", "VERBOSITY=verbose", "-q", "-f", rolesFilePath,
	}
	exitCode, stderr, err := containerExecStreaming(ctx, r.Cli, containerName, cmd,
		ybRolesApplyEnv(r.YBPass), devNull{})
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
	report := classifyYBRolesApply(stderr, rolesFilePath)
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
