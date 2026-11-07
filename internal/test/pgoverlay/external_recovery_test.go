//go:build pgoverlay

package pgoverlay_test

import (
	"regexp"
	"strings"
	"testing"
)

// nameChangeLine matches the plan line that changes the role name.
var nameChangeLine = regexp.MustCompile(`(?m)^\s*~\s+name\s+=`)

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
