// Role restoration applies the exported roles file before the schema to create
// identities for schema privilege statements.
//
// Previously, the drill inferred role names from schema text with a regular
// expression, created each match, and ignored errors. That method omitted
// names outside the pattern without reporting them and created roles without
// privileges. TACK-474 recorded a restored ledger that callers could not read.
// The roles file replaces inference without a fallback that could repeat the
// silent omission.
//
// Database diagnostics depend on message locale. The apply fixes both server
// lc_messages and the client message locale for this connection. Classification
// uses SQLSTATE, a SQL-standard identifier that locale does not change, instead
// of the severity token.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// ybDuplicateObjectSQLState identifies SQLSTATE 42710, duplicate_object,
// which CREATE ROLE reports when the target already has the role.
const ybDuplicateObjectSQLState = "42710"

// ybDuplicateRoleMessagePattern matches the duplicate CREATE ROLE message in
// the apply's C locale. SQLSTATE 42710 includes duplicate objects of every
// kind; the message must establish that the duplicate is a role.
//
// The server inserts names without escaping. Quoted identifiers can contain
// double quotes or newlines. A capture that forbids quotes would truncate
// valid names and fail allowed duplicates. The pattern spans lines after
// ybRenderedDiagnostics reassembles the message.
//
// The final fixed suffix uniquely ends the name after the opening quote. A name
// containing or ending in that suffix must match in full instead of stopping at
// the first occurrence.
var ybDuplicateRoleMessagePattern = regexp.MustCompile(`(?s)^role "(.+)" already exists$`)

// ybRolesApplyReport records roles the target already had and all other errors
// from one roles-file application.
type ybRolesApplyReport struct {
	// AlreadyPresent lists roles that the target rejects in CREATE ROLE because
	// they already exist.
	AlreadyPresent []string
	// Unexpected records the exact opening line of every other error.
	Unexpected []string
}

// ybSQLStateClass represents the first two SQLSTATE characters, which determine
// whether a diagnostic reports failure.
type ybSQLStateClass string

const (
	// ybSQLStateClassSuccess represents class 00, successful completion, which
	// notices use.
	ybSQLStateClassSuccess ybSQLStateClass = "00"
	// ybSQLStateClassWarning represents class 01, a warning rather than a failed
	// statement.
	ybSQLStateClassWarning ybSQLStateClass = "01"
	// ybSQLStateClassNoData represents class 02, an empty result rather than
	// failure.
	ybSQLStateClassNoData ybSQLStateClass = "02"
)

// ybSQLStateIsFailure checks the SQLSTATE class. The SQL standard reserves
// three classes for outcomes without exceptions and treats all other classes
// as exceptions. Reading the class instead of the severity word makes this
// decision independent of message locale.
func ybSQLStateIsFailure(sqlState string) bool {
	switch ybSQLStateClass(sqlState[:2]) {
	case ybSQLStateClassSuccess, ybSQLStateClassWarning, ybSQLStateClassNoData:
		return false
	default:
		return true
	}
}

// ybDuplicateRoleName returns the existing role's name or an empty string if
// the diagnostic does not identify a duplicate role. The SQLSTATE must indicate
// a duplicate object, and the message must identify that object as a role.
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

// classifyYBRolesApply classifies each reassembled diagnostic from stderr.
// rolesFilePath identifies diagnostics about the applied file without relying
// on localized words.
//
// The apply tolerates only roles that already exist. The engine creates
// postgres, yugabyte, yb_db_admin, yb_extension, and yb_fdw during initdb, and
// the target's bootstrap user exists before restore. The roles file includes
// every source-cluster role, including these identities. The dump follows
// CREATE ROLE immediately with ALTER ROLE to apply attributes. ALTER still runs
// when CREATE reports a duplicate and restores the backup attributes.
//
// Other failure diagnostics reject the restore. A failure SQLSTATE with any
// other message counts as unexpected. A psql diagnostic about the roles file
// without SQLSTATE also counts as unexpected because classification requires
// verbose output. Diagnostics that classification cannot interpret stop the
// drill.
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

// ybRolesApplyEnv supplies the password and fixes the diagnostic locale for
// both processes. PGOPTIONS sets the server's lc_messages for severity and
// message text. Client locale variables control labels such as LOCATION and
// DETAIL. Empty LC_ALL clears an image-provided override without changing the
// restored data's collation or character type.
//
// lc_messages requires a superuser. A verification against
// yugabytedb/yugabyte:2025.2.3.0-b149 confirmed that the scratch bootstrap
// user had superuser access and could set it at connection time. Without
// that access, connection refusal would fail the drill instead of permitting
// unreadable diagnostics.
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
// The apply does not enable ON_ERROR_STOP. The complete source role set
// includes roles that the target already has. Stopping at the first duplicate
// would leave the file partly applied and fail every restore. Classification
// instead rejects every failure except a duplicate role. Other role-creation
// failures must fail here rather than produce a ledger that callers cannot
// read. VERBOSITY=verbose includes the SQLSTATE required for classification on
// every diagnostic.
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
