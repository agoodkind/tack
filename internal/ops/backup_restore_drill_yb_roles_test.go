package ops

import (
	"reflect"
	"strings"
	"testing"
)

// liveRolesApplyStderr is the stderr a real roles file produced, captured on
// 2026-08-30 by applying a ysql_dumpall --roles-only --no-role-passwords file
// to a yugabytedb/yugabyte:2024.2.8.0-b85 cluster holding only the roles a
// fresh engine creates plus its bootstrap user. Every line is a role the target
// already had; there is no other error in a healthy apply.
const liveRolesApplyStderr = `ysqlsh:/artifacts/roles.sql:24: ERROR:  42710: role "postgres" already exists
LOCATION:  CreateRole, user.c:348
ysqlsh:/artifacts/roles.sql:26: ERROR:  42710: role "tack" already exists
LOCATION:  CreateRole, user.c:348
ysqlsh:/artifacts/roles.sql:30: ERROR:  42710: role "yb_db_admin" already exists
LOCATION:  CreateRole, user.c:348
ysqlsh:/artifacts/roles.sql:32: ERROR:  42710: role "yb_extension" already exists
LOCATION:  CreateRole, user.c:348
ysqlsh:/artifacts/roles.sql:34: ERROR:  42710: role "yb_fdw" already exists
LOCATION:  CreateRole, user.c:348
ysqlsh:/artifacts/roles.sql:36: ERROR:  42710: role "yugabyte" already exists
LOCATION:  CreateRole, user.c:348
`

// TestClassifyYBRolesApplyPassesOverRolesTheTargetAlreadyHas proves the one
// tolerated class is recognized on real output. Every restore target already
// carries the roles its own engine created at initdb plus its bootstrap user,
// and the roles file describes those too, so refusing them would fail every
// restore. Nothing is lost by passing over the refused CREATE: the dump follows
// each one with an ALTER ROLE that sets the role's attributes, and that still
// runs.
func TestClassifyYBRolesApplyPassesOverRolesTheTargetAlreadyHas(t *testing.T) {
	report := classifyYBRolesApply(liveRolesApplyStderr)
	if len(report.Unexpected) > 0 {
		t.Fatalf("a healthy apply must raise nothing unexpected, got %v", report.Unexpected)
	}
	want := []string{"postgres", "tack", "yb_db_admin", "yb_extension", "yb_fdw", "yugabyte"}
	if !reflect.DeepEqual(report.AlreadyPresent, want) {
		t.Fatalf("already present:\n got=%v\nwant=%v", report.AlreadyPresent, want)
	}
}

// TestClassifyYBRolesApplyFailsClosedOnAnythingElse proves the classification
// is deny-by-default. A roles file that could not give the target a role, for
// any reason other than already having it, must fail the restore here: the
// alternative is a database whose ledger nobody can read, discovered during an
// incident. An error line carrying no SQLSTATE, or a different SQLSTATE, or a
// duplicate object that is not a role, counts as unexpected rather than being
// passed over.
func TestClassifyYBRolesApplyFailsClosedOnAnythingElse(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			name: "insufficient privilege",
			line: `ysqlsh:/artifacts/roles.sql:24: ERROR:  42501: permission denied to create role`,
		},
		{
			name: "no sqlstate because verbosity was not verbose",
			line: `ysqlsh:/artifacts/roles.sql:24: ERROR:  role "audit_reader" already exists`,
		},
		{
			name: "duplicate object that is not a role",
			line: `ysqlsh:/artifacts/roles.sql:24: ERROR:  42710: schema "audit" already exists`,
		},
		{
			name: "grant of a membership the target cannot make",
			line: `ysqlsh:/artifacts/roles.sql:41: ERROR:  42704: role "tack" does not exist`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := classifyYBRolesApply(liveRolesApplyStderr + test.line + "\n")
			if !reflect.DeepEqual(report.Unexpected, []string{test.line}) {
				t.Fatalf("unexpected:\n got=%v\nwant=[%s]", report.Unexpected, test.line)
			}
		})
	}
}

// TestClassifyYBRolesApplyIgnoresNonErrorOutput proves the classification reads
// errors rather than everything the apply prints: the verbose LOCATION lines
// and psql's notices are not failures.
func TestClassifyYBRolesApplyIgnoresNonErrorOutput(t *testing.T) {
	report := classifyYBRolesApply("NOTICE:  role \"audit_reader\" is not a member of role \"tack\"\nLOCATION:  CreateRole, user.c:348\n")
	if len(report.Unexpected) > 0 || len(report.AlreadyPresent) > 0 {
		t.Fatalf("non-error output must classify as nothing, got %+v", report)
	}
}

// TestYBRolesFileContainerPathMatchesTheStagedArtifact proves the drill applies
// the artifact the export publishes. The staged file takes the artifact's own
// object name, so a path built independently here would apply a file that is
// never there and silently leave the target with no roles.
func TestYBRolesFileContainerPathMatchesTheStagedArtifact(t *testing.T) {
	if !strings.HasSuffix(ybRolesFileContainerPath, "/"+ybSnapshotRolesObject) {
		t.Fatalf("roles file path %q does not name the published artifact %q",
			ybRolesFileContainerPath, ybSnapshotRolesObject)
	}
}
