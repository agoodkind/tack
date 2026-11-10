// Role-apply classification reads duplicate-role messages with unescaped role
// names. A role name can include the same characters that delimit the message.
//
// Rejecting an ordinary duplicate makes the drill report a false failure.
// False failures are safer than false passes, but repeated false failures can
// lead operators to disregard a verdict that must detect failed recovery.

package ops

import (
	"reflect"
	"testing"
)

// ybRolesApplyDiagnostic renders the verbose ysqlsh diagnostic format that the
// pinned apply uses. Tests classify a full diagnostic line instead of a bare
// message.
func ybRolesApplyDiagnostic(sqlState, message string) string {
	return "ysqlsh:" + ybTestRolesFilePath + ":24: ERROR:  " + sqlState + ": " + message + "\n"
}

// TestClassifyYBRolesApplyReadsRoleNamesTheMessageShapeCannotDelimit checks
// duplicate-role recognition when names include message delimiters. A quoted
// SQL identifier can include a double quote. The server inserts the name into
// "role %s already exists" without escaping it. Names can also end with the
// fixed message suffix. The capture must include everything before the final
// suffix; stopping earlier would truncate a valid name for an existing role.
func TestClassifyYBRolesApplyReadsRoleNamesTheMessageShapeCannotDelimit(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{name: "a quote inside the name", role: `we"ird`},
		{name: "a quote opening the name", role: `"leading`},
		{name: "the message's own suffix inside the name", role: `ledger" already exists`},
		{name: "the whole message shape inside the name", role: `role "inner" already exists`},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				stderr := ybRolesApplyDiagnostic(
					ybDuplicateObjectSQLState,
					`role "`+test.role+`" already exists`,
				)
				report := classifyYBRolesApply(stderr, ybTestRolesFilePath)
				if len(report.Unexpected) > 0 {
					t.Fatalf("a duplicate role must not fail the drill, got %v", report.Unexpected)
				}

				if !reflect.DeepEqual(report.AlreadyPresent, []string{test.role}) {
					t.Fatalf(
						"already present:\n got=%v\nwant=[%s]",
						report.AlreadyPresent,
						test.role,
					)
				}
			},
		)
	}
}

// liveNewlineRolesApplyStderr records stderr from a real roles file applied
// on 2026-09-01 to yugabytedb/yugabyte:2025.2.3.0-b149. The target already
// had two roles with newlines in their names. The file came from ysql_dumpall
// --roles-only --no-role-passwords, and the apply used the exact command and
// environment of applyYBDrillRoles.
//
// The server inserts role names without escaping, and the client prints those
// names unchanged. A newline splits a diagnostic after part of the name.
// Remaining name text does not have a prefix, label, or indentation;
// consecutive newlines create an empty message line. Only the first line
// contains the file location and SQLSTATE.
const liveNewlineRolesApplyStderr = `ysqlsh:/artifacts/roles.sql:14: ERROR:  42710: role "drill" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:17: ERROR:  42710: role "first
second" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:20: ERROR:  42710: role "postgres" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:24: ERROR:  42710: role "third

fourth" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:28: ERROR:  42710: role "yb_db_admin" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:30: ERROR:  42710: role "yb_extension" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:32: ERROR:  42710: role "yb_fdw" already exists
LOCATION:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:34: ERROR:  42710: role "yugabyte" already exists
LOCATION:  CreateRole, user.c:319
`

// TestClassifyYBRolesApplyReassemblesARoleNameSplitAcrossLines checks duplicate
// recognition using real output with newlines in role names. Quoted SQL
// identifiers can include newlines, and the client preserves them in the server
// message. Classifying lines separately would reject the first fragment without
// a closing suffix and discard the continuation. Reassemble the diagnostic
// before classification to avoid failing an allowed duplicate.
func TestClassifyYBRolesApplyReassemblesARoleNameSplitAcrossLines(t *testing.T) {
	report := classifyYBRolesApply(liveNewlineRolesApplyStderr, ybTestRolesFilePath)
	if len(report.Unexpected) > 0 {
		t.Fatalf("a duplicate role must not fail the drill, got %q", report.Unexpected)
	}

	want := []string{
		"drill",
		"first\nsecond",
		"postgres",
		"third\n\nfourth",
		"yb_db_admin",
		"yb_extension",
		"yb_fdw",
		"yugabyte",
	}
	if !reflect.DeepEqual(report.AlreadyPresent, want) {
		t.Fatalf("already present:\n got=%q\nwant=%q", report.AlreadyPresent, want)
	}
}

// TestClassifyYBRolesApplyStillRefusesADuplicateThatIsNotARole checks that a
// wider name capture does not accept more object types. SQLSTATE 42710 covers
// duplicate objects of every kind. A duplicate schema must fail even when its
// name imitates a duplicate-role message.
func TestClassifyYBRolesApplyStillRefusesADuplicateThatIsNotARole(t *testing.T) {
	message := `schema "role "audit" already exists" already exists`
	stderr := ybRolesApplyDiagnostic(ybDuplicateObjectSQLState, message)
	report := classifyYBRolesApply(stderr, ybTestRolesFilePath)
	if len(report.AlreadyPresent) > 0 {
		t.Fatalf("only a duplicate role may be tolerated, got %v", report.AlreadyPresent)
	}

	want := []string{
		"ysqlsh:" + ybTestRolesFilePath + ":24: ERROR:  " +
			ybDuplicateObjectSQLState + ": " + message,
	}
	if !reflect.DeepEqual(report.Unexpected, want) {
		t.Fatalf("unexpected:\n got=%v\nwant=%v", report.Unexpected, want)
	}
}
