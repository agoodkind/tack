// Restore applies exported cluster roles before schema privilege statements.
// It uses the complete roles artifact rather than inferring identities from
// schema text, which could silently omit roles or privileges (TACK-474).
// Classification uses SQLSTATE with fixed server and client message locales.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybDuplicateObjectSQLState identifies duplicate_object from CREATE ROLE.
const ybDuplicateObjectSQLState = "42710"

// ybDuplicateRoleMessagePattern distinguishes duplicate roles from other
// duplicate objects in the C locale. Names may include quotes or newlines.
// The final anchored suffix selects the complete name even when its text
// includes that suffix. ybRenderedDiagnostics reconstructs multiline messages.
var ybDuplicateRoleMessagePattern = regexp.MustCompile(`(?s)^role "(.+)" already exists$`)

// ybRolesApplyReport records existing roles and unexpected apply errors.
type ybRolesApplyReport struct {
	// AlreadyPresent lists roles that already exist in the target.
	AlreadyPresent []string
	// Unexpected preserves the opening line of every other error.
	Unexpected []string
}

// ybSQLStateClass classifies a diagnostic by SQLSTATE's first two characters.
type ybSQLStateClass string

const (
	// ybSQLStateClassSuccess indicates successful completion, including notices.
	ybSQLStateClassSuccess ybSQLStateClass = "00"
	// ybSQLStateClassWarning indicates warnings rather than failed statements.
	ybSQLStateClassWarning ybSQLStateClass = "01"
	// ybSQLStateClassNoData indicates an empty result rather than failure.
	ybSQLStateClassNoData ybSQLStateClass = "02"
)

// ybSQLStateIsFailure excludes the SQL standard's three nonexception classes.
// SQLSTATE classification does not depend on localized severity text.
func ybSQLStateIsFailure(sqlState string) bool {
	switch ybSQLStateClass(sqlState[:2]) {
	case ybSQLStateClassSuccess, ybSQLStateClassWarning, ybSQLStateClassNoData:
		return false
	default:
		return true
	}
}

// ybDuplicateRoleName requires both duplicate_object SQLSTATE and a role
// message. Other diagnostics return an empty string.
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

// classifyYBRolesApply tolerates only duplicate-role failures. Bootstrap roles
// already exist in the target. The dump's ALTER ROLE still applies their saved
// attributes after a refused CREATE ROLE. Other failure diagnostics and
// roles-file diagnostics without SQLSTATE fail classification. Missing verbose
// output cannot silently suppress a failed statement.
func classifyYBRolesApply(stderr, rolesFilePath string) ybRolesApplyReport {
	report := ybRolesApplyReport{AlreadyPresent: nil, Unexpected: nil}

	for _, diagnostic := range ybRenderedDiagnostics(stderr, rolesFilePath) {
		if diagnostic.SQLState == "" {
			report.Unexpected = append(report.Unexpected, diagnostic.Line)
			continue
		}

		if !ybSQLStateIsFailure(diagnostic.SQLState) {
			continue
		}

		role := ybDuplicateRoleName(diagnostic.SQLState, diagnostic.Message)
		if role == "" {
			report.Unexpected = append(report.Unexpected, diagnostic.Line)
			continue
		}

		report.AlreadyPresent = append(report.AlreadyPresent, role)
	}

	return report
}

// ybRolesApplyEnv fixes server lc_messages and client message labels to C.
// Clearing LC_ALL preserves restored collation and character settings while
// permitting LC_MESSAGES to control diagnostics. The scratch bootstrap user
// has superuser access for lc_messages, verified on
// yugabytedb/yugabyte:2025.2.3.0-b149. If that permission fails, connection
// failure stops the drill rather than permitting unrecognized diagnostics.
func ybRolesApplyEnv(password string) []string {
	return []string{
		"PGPASSWORD=" + password,
		"PGOPTIONS=-c lc_messages=C",
		"LC_ALL=",
		"LC_MESSAGES=C",
	}
}

// applyYBDrillRoles applies the staged artifact without ON_ERROR_STOP to
// permit existing bootstrap roles. Verbose diagnostics include SQLSTATE for
// classification; every failure other than a duplicate role stops the drill.
func applyYBDrillRoles(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	database string,
) error {
	logger := telemetry.L(ctx)
	rolesFilePath := ybDrillArtifactPath(ybSnapshotRolesObject)
	cmd := []string{
		"ysqlsh",
		"-h",
		ybScratchHost(containerName),
		"-p",
		drillLedgerPort,
		"-U",
		database,
		"-d",
		database,
		"-v",
		"VERBOSITY=verbose",
		"-q",
		"-f",
		rolesFilePath,
	}
	exitCode, stderr, err := containerExecStreaming(
		ctx,
		r.Cli,
		containerName,
		cmd,
		ybRolesApplyEnv(r.YBPass),
		devNull{},
	)
	if err != nil {
		wrapped := fmt.Errorf("apply the export's roles file: %w", err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if exitCode != 0 {
		wrapped := fmt.Errorf(
			"apply the export's roles file: ysqlsh exited %d: %s",
			exitCode,
			strings.TrimSpace(stderr),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	report := classifyYBRolesApply(stderr, rolesFilePath)
	if len(report.Unexpected) > 0 {
		wrapped := fmt.Errorf(
			"the export's roles file did not apply cleanly: %s",
			strings.Join(report.Unexpected, "; "),
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.yb.failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.yb.roles_applied",
		slog.Any("already_present", report.AlreadyPresent),
	)

	return nil
}
