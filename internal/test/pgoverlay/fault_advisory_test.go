//go:build pgoverlay

package pgoverlay_test

import "testing"

func TestPostgreSQLAdvisoryReadFailureAfterCommit(t *testing.T) {
	s := postgresqlScenario(t)
	s.requireApplied(t, "1", infinity)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	proxy := newFaultProxy(t, s.target, s.role, "commit")
	s.space.target = proxy.target(s.target)

	output, code := s.applyRole(t, renamed, "2", infinity)
	proxy.recover(t)
	if code == exitSuccess {
		t.Fatalf("the update succeeded after the proxy denied the final read:\n%s", output)
	}
	if !s.target.roleExists(t, renamed) || s.target.roleExists(t, s.role) {
		t.Fatal("the database did not retain exactly the renamed role")
	}
	state := s.space.stateValues(t)
	if state.ID != renamed || state.Version != "1" {
		t.Fatalf("state has id %s and password version %s; the test expects id %s and password version 1", state.ID, state.Version, renamed)
	}
	variables := s.space.roleVariables(renamed, s.password("2"), "2", infinity)
	plan, code := s.space.run(t, variables, "plan", "-detailed-exitcode", "-no-color", "-input=false")
	if code != exitChanges {
		t.Fatalf("the retry plan exited %d and the test expects changes:\n%s", code, plan)
	}
	s.requirePlanRequestsPassword(t, plan)
	if output, code := s.applyRole(t, renamed, "2", infinity); code != exitSuccess {
		t.Fatalf("retry after the final read failure exited %d:\n%s", code, output)
	}
	if err := s.target.loginError(t, renamed, s.password("2")); err != nil {
		t.Fatalf("login with the new password failed: %v", err)
	}
	_, code = s.space.run(t, variables, "plan", "-detailed-exitcode", "-no-color", "-input=false")
	if code != exitSuccess {
		t.Fatalf("the repeated plan exited %d and the test expects no changes", code)
	}
	s.requireNoSecrets(t)
}
