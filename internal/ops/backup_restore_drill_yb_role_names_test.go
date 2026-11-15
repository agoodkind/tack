// These tests check duplicate-role classification for quoted SQL identifiers
// containing message delimiters and newlines. The server inserts role names
// without escaping them in diagnostics.

package ops

import (
	"reflect"
	"testing"
)

// ybRolesApplyDiagnostic formats a verbose ysqlsh diagnostic with a file location.
func ybRolesApplyDiagnostic(sqlState, message string) string {
	return "ysqlsh:" + ybTestRolesFilePath + ":24: ERROR:  " + sqlState + ": " + message + "\n"
}

// TestClassifyYBRolesApplyReadsRoleNamesTheMessageShapeCannotDelimit checks that
// embedded quotes and duplicate-message suffixes do not truncate role names.
func TestClassifyYBRolesApplyReadsRoleNamesTheMessageShapeCannotDelimit(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{name: "a quote inside the name", role: `audit"reader`},
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

// liveNewlineRolesApplyStderr reproduces output captured on 2026-09-01 by
// applying a ysql_dumpall --roles-only --no-role-passwords file to
// yugabytedb/yugabyte:2025.2.3.0-b149 with applyYBDrillRoles's command and
// environment. Embedded newlines split role diagnostics across lines. Only the
// first line includes the file location and SQLSTATE.
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

// TestClassifyYBRolesApplyReassemblesARoleNameSplitAcrossLines checks complete
// role names from captured multiline diagnostics, including an empty line.
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
// duplicate schema cannot imitate an accepted duplicate-role diagnostic.
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
