//go:build pgoverlay

package pgoverlay_test

import (
	"regexp"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

const (
	externalMode = "external"
	exitFailure  = 1
)

// nameChangeLine matches the plan line that changes the role name.
var nameChangeLine = regexp.MustCompile(`(?m)^\s*~\s+name\s+=`)

// externalScenario returns a scenario on the pinned YugabyteDB engine with the
// provider in external lock mode.
func externalScenario(t *testing.T) *scenario {
	t.Helper()
	s := newScenario(t, parseTarget(t, testenv.Ledger(t), yugabyteExpectedVersion))
	s.space.lockMode = externalMode
	return s
}

func (s *scenario) requireStateOID(t *testing.T, want int64) {
	t.Helper()
	got := s.space.stateValues(t)
	if got.OID != want {
		t.Fatalf("state has role_oid %d and the database has %d", got.OID, want)
	}
}

// planRole runs tofu plan for a role name that can differ from the name in state.
func (s *scenario) planRole(t *testing.T, role, version, validUntil string, want int) string {
	t.Helper()
	variables := s.space.roleVariables(role, s.password(version), version, validUntil)
	output, code := s.space.run(t, variables, "plan", "-detailed-exitcode", "-no-color", "-input=false")
	if code != want {
		t.Fatalf("plan exited %d and the test expects %d:\n%s", code, want, output)
	}
	return output
}

// refreshOnly runs a refresh-only apply, which saves refreshed state.
func (s *scenario) refreshOnly(t *testing.T, role, version, validUntil string) {
	t.Helper()
	variables := s.space.roleVariables(role, s.password(version), version, validUntil)
	output, code := s.space.run(t, variables, "apply", "-refresh-only", "-auto-approve", "-no-color", "-input=false")
	if code != exitSuccess {
		t.Fatalf("refresh-only apply exited %d:\n%s", code, output)
	}
}

// TestProviderRejectsUnknownRoleLockMode sets a role_lock_mode value that the
// provider does not support.
func TestProviderRejectsUnknownRoleLockMode(t *testing.T) {
	s := postgresqlScenario(t)
	s.space.lockMode = "unsupported"
	output, code := s.plan(t, "1", infinity)
	if code != exitFailure || !strings.Contains(output, "role_lock_mode") {
		t.Fatalf("plan exited %d and the test expects a role_lock_mode error:\n%s", code, output)
	}
}

// TestYugabyteExternalRejectsRoleMemberships configures the roles attribute in
// external mode.
func TestYugabyteExternalRejectsRoleMemberships(t *testing.T) {
	s := externalScenario(t)
	s.space.memberships = `["pg_monitor"]`
	output, code := s.plan(t, "1", infinity)
	if code != exitFailure || !strings.Contains(output, "roles is not supported") {
		t.Fatalf("plan exited %d and the test expects the roles diagnostic:\n%s", code, output)
	}
}

// TestYugabyteExternalPasswordRotation rotates the password of an existing role
// in external mode while a membership that the test granted stays in place.
func TestYugabyteExternalPasswordRotation(t *testing.T) {
	s := externalScenario(t)
	s.requireApplied(t, "1", infinity)
	oid := s.target.roleOID(t, s.role)
	s.requireStateOID(t, oid)
	sentinel := randomText(t, "sentinel_")
	t.Cleanup(func() { s.target.dropRole(t, sentinel) })
	s.target.createRole(t, sentinel, "")
	s.target.grantMembership(t, sentinel, s.role)

	s.requireApplied(t, "2", infinity)
	s.requireLogin(t, "2", true)
	s.requireLogin(t, "1", false)
	s.requirePlan(t, "2", infinity, exitSuccess)
	if !s.target.isMember(t, s.role, sentinel) {
		t.Fatalf("the sentinel membership of %s does not exist after the password update", s.role)
	}
	s.requireState(t, "2", infinity)
	s.requireStateOID(t, oid)
	s.requireSavedPlanClean(t, "2", infinity)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalImport imports a role that exists in the database and
// requires the state to record its OID.
func TestYugabyteExternalImport(t *testing.T) {
	s := externalScenario(t)
	s.target.createRole(t, s.role, s.password("1"))
	oid := s.target.roleOID(t, s.role)
	variables := s.space.roleVariables(s.role, s.password("1"), "1", infinity)
	output, code := s.space.run(t, variables, "import", "-no-color", "-input=false", "postgresql_role.app", s.role)
	if code != exitSuccess {
		t.Fatalf("import exited %d:\n%s", code, output)
	}
	s.requireStateOID(t, oid)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalDelete destroys a role in external mode.
func TestYugabyteExternalDelete(t *testing.T) {
	s := externalScenario(t)
	s.requireApplied(t, "1", infinity)
	variables := s.space.roleVariables(s.role, s.password("1"), "1", infinity)
	output, code := s.space.run(t, variables, "destroy", "-auto-approve", "-no-color", "-input=false")
	if code != exitSuccess {
		t.Fatalf("destroy exited %d:\n%s", code, output)
	}
	if s.target.roleExists(t, s.role) {
		t.Fatalf("role %s exists after destroy", s.role)
	}
	if count := s.space.stateResourceCount(t); count != 0 {
		t.Fatalf("state has %d resources after destroy", count)
	}
	s.requireNoSecrets(t)
}

// TestYugabyteExternalPasswordThenFailure changes the password version and
// fails the same update at a later statement.
func TestYugabyteExternalPasswordThenFailure(t *testing.T) {
	s := externalScenario(t)
	s.requireApplied(t, "1", infinity)
	output, code := s.apply(t, "2", invalidValidDate)
	if code == exitSuccess {
		t.Fatalf("apply succeeded with an invalid valid_until value:\n%s", output)
	}
	s.requireState(t, "1", infinity)
	s.requirePlanRequestsPassword(t, s.requirePlan(t, "2", infinity, exitChanges))
	s.requireApplied(t, "2", infinity)
	s.requireLogin(t, "2", true)
	s.requireLogin(t, "1", false)
	s.requirePlan(t, "2", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalRenameThenFailure renames a role and fails the same
// update at a later statement. The test reads the actual name from the database
// and requires the refreshed state and plan to follow it.
func TestYugabyteExternalRenameThenFailure(t *testing.T) {
	s := externalScenario(t)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	s.requireApplied(t, "1", infinity)
	oid := s.target.roleOID(t, s.role)

	output, code := s.applyRole(t, renamed, "2", invalidValidDate)
	if code == exitSuccess {
		t.Fatalf("apply succeeded with an invalid valid_until value:\n%s", output)
	}
	actual, found := s.target.roleNameByOID(t, oid)
	if !found || (actual != s.role && actual != renamed) {
		t.Fatalf("role OID %d has name %q (found %t) after the failed update", oid, actual, found)
	}
	if count := s.target.roleCount(t, s.role, renamed); count != 1 {
		t.Fatalf("the database has %d roles named %s or %s, and the test expects 1", count, s.role, renamed)
	}

	s.refreshOnly(t, renamed, "2", infinity)
	got := s.space.stateValues(t)
	if got.Name != actual || got.ID != actual || got.OID != oid || got.Version != "1" {
		t.Fatalf("refreshed state is %+v; the test expects name and id %s, role_oid %d, and password version 1", got, actual, oid)
	}
	plan := s.planRole(t, renamed, "2", infinity, exitChanges)
	s.requirePlanRequestsPassword(t, plan)
	if renamedInPlan := nameChangeLine.MatchString(plan); renamedInPlan != (actual != renamed) {
		t.Fatalf("the plan renames the role: %t; the database name is %s and the target name is %s:\n%s",
			renamedInPlan, actual, renamed, plan)
	}

	if output, code := s.applyRole(t, renamed, "2", infinity); code != exitSuccess {
		t.Fatalf("corrected apply exited %d:\n%s", code, output)
	}
	s.requireConvergedAfterRename(t, oid, renamed)
}

// requireConvergedAfterRename requires one role with the original OID under the
// new name, matching state, a working new password, and an empty plan.
func (s *scenario) requireConvergedAfterRename(t *testing.T, oid int64, renamed string) {
	t.Helper()
	if name, found := s.target.roleNameByOID(t, oid); !found || name != renamed {
		t.Fatalf("role OID %d has name %q (found %t), and the test expects %s", oid, name, found, renamed)
	}
	if count := s.target.roleCount(t, s.role, renamed); count != 1 {
		t.Fatalf("the database has %d roles named %s or %s, and the test expects 1", count, s.role, renamed)
	}
	got := s.space.stateValues(t)
	if got.Name != renamed || got.ID != renamed || got.OID != oid || got.Version != "2" {
		t.Fatalf("state is %+v; the test expects name and id %s, role_oid %d, and password version 2", got, renamed, oid)
	}
	if err := s.target.loginError(t, renamed, s.password("2")); err != nil {
		t.Fatalf("a login as %s with the new password failed: %v", renamed, err)
	}
	s.planRole(t, renamed, "2", infinity, exitSuccess)
	s.requireNoSecrets(t)
}

// TestYugabyteExternalReplacedRole removes a role and creates another role with
// the same name. The provider must report absence for the removed role and must
// refuse to adopt the replacement.
func TestYugabyteExternalReplacedRole(t *testing.T) {
	s := externalScenario(t)
	s.requireApplied(t, "1", infinity)
	original := s.target.roleOID(t, s.role)

	s.target.dropRole(t, s.role)
	plan := s.planRole(t, s.role, "1", infinity, exitChanges)
	if !strings.Contains(plan, "will be created") {
		t.Fatalf("the plan for an absent role does not create it:\n%s", plan)
	}

	s.target.createRole(t, s.role, s.password("2"))
	replacement := s.target.roleOID(t, s.role)
	if replacement == original {
		t.Fatalf("the replacement role has the original OID %d", original)
	}
	for _, args := range [][]string{
		{"plan", "-no-color", "-input=false"},
		{"apply", "-auto-approve", "-no-color", "-input=false"},
	} {
		variables := s.space.roleVariables(s.role, s.password("2"), "2", infinity)
		output, code := s.space.run(t, variables, args...)
		if code != exitFailure || !strings.Contains(output, "role identity conflict") {
			t.Fatalf("tofu %s exited %d and the test expects the identity conflict:\n%s", args[0], code, output)
		}
	}
	if got := s.target.roleOID(t, s.role); got != replacement {
		t.Fatalf("the replacement role has OID %d after the refused operations, and it had %d", got, replacement)
	}
	s.requireStateOID(t, original)
	if err := s.target.loginError(t, s.role, s.password("2")); err != nil {
		t.Fatalf("the replacement role password changed: %v", err)
	}
	s.requireNoSecrets(t)
}
