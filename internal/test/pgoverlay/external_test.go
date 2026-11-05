//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

const (
	externalMode = "external"
	exitFailure  = 1
)

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
