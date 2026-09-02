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
