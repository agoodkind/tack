//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"
	"time"
)

const renameWatchTimeout = 5 * time.Minute

// TestYugabyteExternalInterruptedRename kills the tofu process tree after the
// database renamed the role and before tofu wrote the new state. The next apply
// must finish with the original role OID and without a second role.
func TestYugabyteExternalInterruptedRename(t *testing.T) {
	s := externalScenario(t)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	s.requireApplied(t, "1", infinity)
	oid := s.target.roleOID(t, s.role)
	s.requireStateOID(t, oid)

	variables := s.space.roleVariables(renamed, s.password("2"), "2", infinity)
	process := s.space.start(t, variables, "apply", "-auto-approve", "-no-color", "-input=false")

	tofuPID := findTofuApply(t, s.space.host.Directory)
	waitForProvider(t, tofuPID)
	pacer := startPacer(tofuPID)
	t.Cleanup(func() {
		if err := pacer.freeze(); err != nil {
			t.Errorf("read provider processes during cleanup: %v", err)
		}
		killTree(t, processTree(t, tofuPID))
		_, _ = process.Wait(t)
	})
	s.target.waitForRoleName(t, oid, renamed, renameWatchTimeout)
	if err := pacer.freeze(); err != nil {
		t.Fatalf("read provider processes: %v", err)
	}
	requireProvidersStopped(t, tofuPID)
	if state := processState(t, tofuPID); state == "Z" {
		t.Fatalf(
			"tofu ended before the interruption, and the test expects it to wait for the provider",
		)
	}
	s.target.waitForRoleName(t, oid, renamed, renameWatchTimeout)
	tree := processTree(t, tofuPID)
	if signaled := killTree(t, tree); signaled < 2 {
		t.Fatalf(
			"the interruption signaled %d processes, and the test expects tofu and its provider",
			signaled,
		)
	}
	output, code := s.space.wait(t, process)
	s.requireInterruptedBeforeStatePersisted(t, output, code, oid)
	s.target.waitForQuietStatements(t, s.role, renamed)

	if output, code := s.applyRole(t, renamed, "2", infinity); code != exitSuccess {
		t.Fatalf("the apply after the interruption exited %d:\n%s", code, output)
	}
	s.requireConvergedAfterRename(t, oid, renamed)
}

// requireInterruptedBeforeStatePersisted requires proof that the interruption
// ended the apply after the database rename: the exit status is a failure, the
// output has no completion line, and the state file still records the original
// role name and password version. A test run that loses the race against a
// completing apply fails here.
func (s *scenario) requireInterruptedBeforeStatePersisted(
	t *testing.T,
	output string,
	code int,
	oid int64,
) {
	t.Helper()
	if code == exitSuccess || strings.Contains(output, "Apply complete") {
		t.Fatalf(
			"the apply completed (exit %d) and the test expects the interruption:\n%s",
			code,
			output,
		)
	}
	got := s.space.stateValues(t)
	if got.Name != s.role || got.ID != s.role || got.Version != "1" || got.OID != oid {
		t.Fatalf(
			"the state file is %+v after the interruption; the test expects the original name %s, password version 1, and role_oid %d",
			got,
			s.role,
			oid,
		)
	}
}
