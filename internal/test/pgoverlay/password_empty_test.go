//go:build pgoverlay

package pgoverlay_test

import "testing"

func TestYugabyteExternalEmptyWriteOnlyPassword(t *testing.T) {
	s := externalScenario(t)
	s.firstPassword = ""
	s.requireApplied(t, "1", infinity)
	oid := s.target.roleOID(t, s.role)
	s.requireState(t, "1", infinity)
	s.requireStateOID(t, oid)

	_, first := s.plan(t, "1", infinity)
	second := first
	if first == exitChanges {
		s.requireApplied(t, "1", infinity)
		s.requireState(t, "1", infinity)
		s.requireStateOID(t, oid)
		_, second = s.plan(t, "1", infinity)
	}
	if first != exitSuccess || second != exitSuccess {
		t.Fatalf("repeat plans exited %d and %d; both must show no changes", first, second)
	}
	s.firstPassword = randomText(t, "password-")
	s.requirePlan(t, "1", infinity, exitChanges)
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	s.requireStateOID(t, oid)
	s.requirePlan(t, "1", infinity, exitSuccess)
	s.requireNoSecrets(t)
}
