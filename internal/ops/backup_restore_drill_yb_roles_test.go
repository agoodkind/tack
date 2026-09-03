package ops

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// ybTestRolesFilePath is the path the fixtures below were applied from, which
// classification needs so it can recognize a line psql raised about that file.
const ybTestRolesFilePath = "/artifacts/roles.sql"

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

// liveGermanRolesApplyStderr is the same kind of apply against a cluster whose
// message locale is not English, captured live on 2026-08-30 from
// yugabytedb/yugabyte:2025.2.3.0-b149 with the server's lc_messages set to
// de_DE.utf8, a locale that image ships a message catalog for. Both the severity token and the message text change; the
// SQLSTATE does not.
//
// The two lines are deliberately different failures. The first is the one a
// healthy apply always raises and the drill tolerates when it can read it. The
// second is a role the file could not grant, which must never be tolerated:
// under a classification that reads the English words, neither line is seen at
// all and the apply is reported clean.
const liveGermanRolesApplyStderr = `ysqlsh:/artifacts/roles.sql:1: FEHLER:  42710: Rolle »tack« existiert bereits
ORT:  CreateRole, user.c:319
ysqlsh:/artifacts/roles.sql:5: FEHLER:  42704: Rolle »role_that_is_absent« existiert nicht
ORT:  get_role_oid, acl.c:5194
`

// TestClassifyYBRolesApplyPassesOverRolesTheTargetAlreadyHas proves the one
// tolerated class is recognized on real output. Every restore target already
// carries the roles its own engine created at initdb plus its bootstrap user,
// and the roles file describes those too, so refusing them would fail every
// restore. Nothing is lost by passing over the refused CREATE: the dump follows
// each one with an ALTER ROLE that sets the role's attributes, and that still
// runs.
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

// TestClassifyYBRolesApplyRefusesDiagnosticsItCannotRead drives the
// classification with real diagnostics from a cluster whose message locale is
// not English.
//
// This is the failure the pinned rendering exists to remove, and the reason the
// classification refuses whatever it cannot account for. Reading the English
// severity token would see neither line here: not the tolerable duplicate role,
// and not the missing role that means the restored ledger has a grant it could
// not make. An apply reported clean is worse than one reported broken, because
// the drill's whole purpose is to be the place that failure surfaces.
func TestClassifyYBRolesApplyRefusesDiagnosticsItCannotRead(t *testing.T) {
	report := classifyYBRolesApply(liveGermanRolesApplyStderr, ybTestRolesFilePath)
	if len(report.AlreadyPresent) > 0 {
		t.Fatalf("no diagnostic in an unpinned rendering may be tolerated, got %v", report.AlreadyPresent)
	}
	want := []string{
		`ysqlsh:/artifacts/roles.sql:1: FEHLER:  42710: Rolle »tack« existiert bereits`,
		`ysqlsh:/artifacts/roles.sql:5: FEHLER:  42704: Rolle »role_that_is_absent« existiert nicht`,
	}
	if !reflect.DeepEqual(report.Unexpected, want) {
		t.Fatalf("unexpected:\n got=%v\nwant=%v", report.Unexpected, want)
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
		{
			name: "the roles file could not be read at all",
			line: `ysqlsh: error: /artifacts/roles.sql: No such file or directory`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := classifyYBRolesApply(liveRolesApplyStderr+test.line+"\n", ybTestRolesFilePath)
			if !reflect.DeepEqual(report.Unexpected, []string{test.line}) {
				t.Fatalf("unexpected:\n got=%v\nwant=[%s]", report.Unexpected, test.line)
			}
		})
	}
}

// TestClassifyYBRolesApplyIgnoresNonErrorOutput proves the classification reads
// failures rather than everything the apply prints. The verbose LOCATION lines
// and psql's notices are not failures, and they stay non-failures in a locale
// that renames both of them, because what says so is the notice's SQLSTATE
// class rather than the word in front of it. Both notices are live output from
// yugabytedb/yugabyte:2025.2.3.0-b149.
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

// TestYBRolesApplyEnvPinsBothHalvesOfTheRendering proves the apply asks for the
// one rendering the classification is written against, rather than accepting
// whatever the recovery cluster and the image are set to.
//
// The two halves are pinned by different mechanisms because they are produced
// by different processes: the severity token and the message text come from the
// server, which renders them in its own lc_messages, and the LOCATION and
// DETAIL labels come from the client. Verified live on
// yugabytedb/yugabyte:2025.2.3.0-b149: with the server set to de_DE.utf8,
// PGOPTIONS returns the messages to English, and with LC_ALL=de_DE.utf8 in the
// container, the empty LC_ALL plus LC_MESSAGES returns the labels to English.
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

// TestYBDrillArtifactPathsCoverEveryRequiredArtifact proves the fixed paths the
// restore opens are built from the same object names the completeness gate
// makes a manifest declare. A path built independently of that list would read
// a file no gate required, which is how a run passes selection and then fails
// deep in the restore.
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
