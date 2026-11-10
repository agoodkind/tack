package ops

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// ybTestRolesFilePath specifies the fixture apply path that classification uses
// to recognize psql diagnostics about that file.
const ybTestRolesFilePath = "/artifacts/roles.sql"

// liveRolesApplyStderr records stderr captured on 2026-08-30 from a
// real roles-file apply. The file came from ysql_dumpall --roles-only
// --no-role-passwords. The yugabytedb/yugabyte:2024.2.8.0-b85 target had only
// engine-created roles and its bootstrap user. Every recorded diagnostic
// reports an existing role; a healthy apply does not report another error.
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

// liveGermanRolesApplyStderr records apply diagnostics captured on 2026-08-30
// from yugabytedb/yugabyte:2025.2.3.0-b149 with server lc_messages set to
// de_DE.utf8. That image includes the locale's message catalog. Severity and
// message text change, but SQLSTATE does not.
//
// The first diagnostic reports a duplicate role that a healthy apply must
// tolerate when it can interpret the message. The second reports a failed role
// grant that must fail the drill. Classification based on English words would
// miss both lines and incorrectly accept the apply.
const liveGermanRolesApplyStderr = `ysqlsh:/artifacts/roles.sql:1: FEHLER:  42710: Rolle »tack« existiert bereits
ORT:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:5: FEHLER:  42704: Rolle »role_that_is_absent« existiert nicht
ORT:  get_role_oid, acl.c:5194
`

// TestClassifyYBRolesApplyPassesOverRolesTheTargetAlreadyHas checks the allowed
// duplicate class against real output. Engine-created roles and the bootstrap
// user already exist on every target, and the exported roles file includes
// them. Rejecting these duplicates would fail every restore. Each CREATE in
// the dump has a following ALTER ROLE that still applies the source attributes
// after a duplicate.
func TestClassifyYBRolesApplyPassesOverRolesTheTargetAlreadyHas(t *testing.T) {
	report := classifyYBRolesApply(liveRolesApplyStderr, ybTestRolesFilePath)
	if len(report.Unexpected) > 0 {
		t.Fatalf("a healthy apply must raise nothing unexpected, got %v", report.Unexpected)
	}

	want := []string{"postgres", "tack", "yb_db_admin", "yb_extension", "yb_fdw", "yugabyte"}
	if !reflect.DeepEqual(report.AlreadyPresent, want) {
		t.Fatalf("already present:\n got=%v\nwant=%v", report.AlreadyPresent, want)
	}
}

// TestClassifyYBRolesApplyRefusesDiagnosticsItCannotRead uses real diagnostics
// from a non-English message locale. Locale pinning prevents this rendering,
// and classification must reject it if it occurs. Reading only English severity
// words would miss both the allowed duplicate and the missing-role grant
// failure. The drill must report that failure instead of falsely accepting a
// restored ledger with an unapplied grant.
func TestClassifyYBRolesApplyRefusesDiagnosticsItCannotRead(t *testing.T) {
	report := classifyYBRolesApply(liveGermanRolesApplyStderr, ybTestRolesFilePath)
	if len(report.AlreadyPresent) > 0 {
		t.Fatalf(
			"no diagnostic in an unpinned rendering may be tolerated, got %v",
			report.AlreadyPresent,
		)
	}

	want := []string{
		`ysqlsh:/artifacts/roles.sql:1: FEHLER:  42710: Rolle »tack« existiert bereits`,
		`ysqlsh:/artifacts/roles.sql:5: FEHLER:  42704: Rolle »role_that_is_absent« existiert nicht`,
	}
	if !reflect.DeepEqual(report.Unexpected, want) {
		t.Fatalf("unexpected:\n got=%v\nwant=%v", report.Unexpected, want)
	}
}

// TestClassifyYBRolesApplyFailsClosedOnAnythingElse checks rejection of
// errors outside the duplicate-role class. Any other role restoration failure
// must fail before an incident reveals a ledger that callers cannot read.
// Missing SQLSTATE, another SQLSTATE, and a duplicate object other than a role
// all count as unexpected.
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
		{
			name: "the roles file could not be read at all",
			line: `ysqlsh: error: /artifacts/roles.sql: No such file or directory`,
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				report := classifyYBRolesApply(
					liveRolesApplyStderr+test.line+"\n",
					ybTestRolesFilePath,
				)
				if !reflect.DeepEqual(report.Unexpected, []string{test.line}) {
					t.Fatalf("unexpected:\n got=%v\nwant=[%s]", report.Unexpected, test.line)
				}
			},
		)
	}
}

// TestClassifyYBRolesApplyIgnoresNonErrorOutput checks that verbose
// LOCATION lines and psql notices do not count as failures, including
// localized versions. Notice SQLSTATE classes determine that result
// independently of localized words. Both notice fixtures came from live
// yugabytedb/yugabyte:2025.2.3.0-b149 output.
func TestClassifyYBRolesApplyIgnoresNonErrorOutput(t *testing.T) {
	lines := []string{
		`NOTICE:  00000: role "never_existed" does not exist, skipping`,
		`HINWEIS:  00000: Rolle »never_existed« existiert nicht, wird übersprungen`,
		`LOCATION:  DropRole, user.c:1112`,
		`ORT:  DropRole, user.c:1112`,
		`WARNING:  01000: a warning is not a failed statement`,
	}
	for _, line := range lines {
		report := classifyYBRolesApply(line+"\n", ybTestRolesFilePath)
		if len(report.Unexpected) > 0 || len(report.AlreadyPresent) > 0 {
			t.Errorf("%q must classify as nothing, got %+v", line, report)
		}
	}
}

// TestYBRolesApplyEnvPinsBothHalvesOfTheRendering checks that the apply
// requests the diagnostic format classification requires. The server renders
// severity and message text using lc_messages; the client renders LOCATION
// and DETAIL labels. A live check against yugabytedb/yugabyte:2025.2.3.0-b149
// confirmed that PGOPTIONS restored English messages from server de_DE.utf8.
// With container LC_ALL=de_DE.utf8, empty LC_ALL plus LC_MESSAGES restored
// English client labels.
func TestYBRolesApplyEnvPinsBothHalvesOfTheRendering(t *testing.T) {
	env := ybRolesApplyEnv("drill-pass") // gitleaks:allow test placeholder

	for _, required := range []string{
		"PGPASSWORD=drill-pass", // gitleaks:allow test placeholder
		"PGOPTIONS=-c lc_messages=C",
		"LC_ALL=",
		"LC_MESSAGES=C",
	} {
		if !slices.Contains(env, required) {
			t.Errorf("the roles apply does not carry %q: %v", required, env)
		}
	}
}

// TestYBDrillArtifactPathsCoverEveryRequiredArtifact checks that fixed restore
// paths use the object names required by the manifest completeness gate.
// An independent path could require a file that selection never checked and
// fail later during restore.
func TestYBDrillArtifactPathsCoverEveryRequiredArtifact(t *testing.T) {
	for _, artifact := range ybRequiredRunArtifacts() {
		path := ybDrillArtifactPath(artifact)
		if !strings.HasSuffix(path, "/"+artifact) {
			t.Errorf("staged path %q does not name the artifact %q", path, artifact)
		}

		if !strings.HasPrefix(path, ybDrillArtifactMount+"/") {
			t.Errorf("staged path %q is not under the bind-mounted stage dir", path)
		}
	}
}
