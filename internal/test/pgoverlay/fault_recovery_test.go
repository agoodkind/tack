//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"
)

func TestYugabyteExternalReadFailureAfterCommit(t *testing.T) {
	verifyReadFailureAfterCommit(t, externalScenario(t))
}

func verifyReadFailureAfterCommit(t *testing.T, s *scenario) {
	t.Helper()
	s.requireApplied(t, "1", infinity)
	oid := s.target.roleOID(t, s.role)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	proxy := newFaultProxy(t, s.target, s.role, "commit")
	s.space.target = proxy.target(s.target)

	output, code := s.applyRole(t, renamed, "2", infinity)
	proxy.recover(t)
	if code == exitSuccess || !strings.Contains(output, "could not read the name of role OID") {
		t.Fatalf("the update exited %d; the test expects a final role read error:\n%s", code, output)
	}
	actual, found := s.target.roleNameByOID(t, oid)
	if !found || actual != renamed {
		t.Fatalf("the database role OID %d has name %q and found is %t; the test expects %s", oid, actual, found, renamed)
	}
	got := s.space.stateValues(t)
	if got.ID != renamed || got.OID != oid || got.Version != "1" {
		t.Fatalf("state is %+v; the test expects id %s, role_oid %d, and password version 1", got, renamed, oid)
	}
	s.requirePlanRequestsPassword(t, s.planRole(t, renamed, "2", infinity, exitChanges))
	if output, code := s.applyRole(t, renamed, "2", infinity); code != exitSuccess {
		t.Fatalf("retry after the final read failure exited %d:\n%s", code, output)
	}
	s.requireConvergedAfterRename(t, oid, renamed)
}

func TestYugabyteExternalCreateDisconnectAfterOID(t *testing.T) {
	s := externalScenario(t)
	proxy := newFaultProxy(t, s.target, s.role, "capture")
	s.space.target = proxy.target(s.target)

	output, code := s.apply(t, "1", infinity)
	proxy.recover(t)
	if code == exitSuccess {
		t.Fatalf("the create succeeded after the proxy denied further database operations:\n%s", output)
	}
	if !s.target.roleExists(t, s.role) {
		t.Fatal("the database did not retain the role after OID capture")
	}
	oid := s.target.roleOID(t, s.role)
	got := s.space.stateValues(t)
	if got.ID != s.role || got.OID != oid {
		t.Fatalf("state is %+v; the test expects id %s and role_oid %d", got, s.role, oid)
	}
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	s.requirePlan(t, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}
