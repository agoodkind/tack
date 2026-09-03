// backup_restore_drill_yb_role_names_test.go covers what the roles-apply
// classification does with role names the message shape does not obviously
// survive. The tolerated class is read out of a message the server builds by
// interpolating the role's own name, unescaped, so the name can carry the
// characters the message uses as its own structure.
//
// A drill that fails on an ordinary duplicate is a false failure rather than a
// false pass, which is the safer direction but not a harmless one: an operator
// who learns to disregard the drill's verdict is protected by nothing.

package ops

import (
	"reflect"
	"testing"
)

// ybRolesApplyDiagnostic renders one verbose ysqlsh diagnostic the way the
// pinned apply prints it, so these tests drive the classification over the same
// line shape a real apply produces rather than over a bare message.
func ybRolesApplyDiagnostic(sqlState, message string) string {
	return "ysqlsh:" + ybTestRolesFilePath + ":24: ERROR:  " + sqlState + ": " + message + "\n"
}

// TestClassifyYBRolesApplyReadsRoleNamesTheMessageShapeCannotDelimit proves the
// tolerated duplicate is still recognized when the role's name carries the
// characters the message is built from.
//
// A quoted SQL identifier may hold a double quote, and the server interpolates
// the name into "role %s already exists" without escaping it, so the message
// carries more quotes than its shape suggests. A name that itself ends in the
// message's own fixed suffix is the same problem one step further on: the
// capture has to span to the last suffix, not the first, or it hands back a
// truncated name for a role the target genuinely already has.
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
		t.Run(test.name, func(t *testing.T) {
			stderr := ybRolesApplyDiagnostic(ybDuplicateObjectSQLState,
				`role "`+test.role+`" already exists`)
			report := classifyYBRolesApply(stderr, ybTestRolesFilePath)
			if len(report.Unexpected) > 0 {
				t.Fatalf("a duplicate role must not fail the drill, got %v", report.Unexpected)
			}
			if !reflect.DeepEqual(report.AlreadyPresent, []string{test.role}) {
				t.Fatalf("already present:\n got=%v\nwant=[%s]", report.AlreadyPresent, test.role)
			}
		})
	}
}

// liveNewlineRolesApplyStderr is the stderr a real roles file produced when the
// target already held two roles whose names carry newlines, captured on
// 2026-09-01 by applying a ysql_dumpall --roles-only --no-role-passwords file
// to yugabytedb/yugabyte:2025.2.3.0-b149 (the configured restore-drill image)
// under the exact command and environment applyYBDrillRoles uses.
//
// The server interpolates the name into the message unescaped and the client
// prints the message as it is, so a name's newline splits the diagnostic across
// lines: the first line ends mid-name, the rest of the name follows with no
// prefix, label, or indent, and a name with two consecutive newlines puts an
// empty line inside the message. Only the first line carries the file locus
// and the SQLSTATE.
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

// TestClassifyYBRolesApplyReassemblesARoleNameSplitAcrossLines proves the
// tolerated duplicate is still recognized when the role's name carries a
// newline, on the real rendering of that case.
//
// A quoted SQL identifier may hold a newline, and the client prints the
// server's message with the newline in it, so the one diagnostic arrives split
// across lines. Classifying each line alone sees a first fragment with no
// closing suffix, which it cannot tolerate, and a rest that matches nothing,
// which it drops: the drill fails on the one class it exists to pass over.
// The diagnostic has to be put back together before it is read.
func TestClassifyYBRolesApplyReassemblesARoleNameSplitAcrossLines(t *testing.T) {
	report := classifyYBRolesApply(liveNewlineRolesApplyStderr, ybTestRolesFilePath)
	if len(report.Unexpected) > 0 {
		t.Fatalf("a duplicate role must not fail the drill, got %q", report.Unexpected)
	}
	want := []string{
		"drill", "first\nsecond", "postgres", "third\n\nfourth",
		"yb_db_admin", "yb_extension", "yb_fdw", "yugabyte",
	}
	if !reflect.DeepEqual(report.AlreadyPresent, want) {
		t.Fatalf("already present:\n got=%q\nwant=%q", report.AlreadyPresent, want)
	}
}

// TestClassifyYBRolesApplyStillRefusesADuplicateThatIsNotARole proves the
// widened capture did not widen what is tolerated. SQLSTATE 42710 covers
// duplicate objects of every kind, and only a duplicate role may be passed
// over, so a duplicate schema whose name is built to look like the role message
// still fails the drill.
func TestClassifyYBRolesApplyStillRefusesADuplicateThatIsNotARole(t *testing.T) {
	message := `schema "role "audit" already exists" already exists`
	stderr := ybRolesApplyDiagnostic(ybDuplicateObjectSQLState, message)
	report := classifyYBRolesApply(stderr, ybTestRolesFilePath)
	if len(report.AlreadyPresent) > 0 {
		t.Fatalf("only a duplicate role may be tolerated, got %v", report.AlreadyPresent)
	}
	want := []string{"ysqlsh:" + ybTestRolesFilePath + ":24: ERROR:  " +
		ybDuplicateObjectSQLState + ": " + message}
	if !reflect.DeepEqual(report.Unexpected, want) {
		t.Fatalf("unexpected:\n got=%v\nwant=%v", report.Unexpected, want)
	}
}
