//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// yugabyteExpectedVersion is the PostgreSQL version that YugabyteDB 2024.2
// is compatible with. The provider cannot parse the engine version string, so
// the test sets expected_version and the provider skips version detection.
const yugabyteExpectedVersion = "11.2.0"

// TestYugabyteAdvisoryLockFailure updates a role on the pinned YugabyteDB
// engine. The provider takes an advisory lock before the update, and the engine
// refuses it. The test requires the engine error in the apply output, the prior
// password still working, the prior password version in state, and a plan that
// requests the update.
func TestYugabyteAdvisoryLockFailure(t *testing.T) {
	s := newScenario(t, parseTarget(t, testenv.Ledger(t), yugabyteExpectedVersion))
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	output, code := s.apply(t, "2", infinity)
	if code == exitSuccess {
		t.Fatalf("the update succeeded and the test expects the advisory lock error:\n%s", output)
	}
	if !strings.Contains(strings.ToLower(output), "advisory") {
		t.Fatalf("the apply output does not show the advisory lock error:\n%s", output)
	}
	s.requireLogin(t, "1", true)
	s.requireLogin(t, "2", false)
	s.requireState(t, "1", infinity)
	s.requirePlanRequestsPassword(t, s.requirePlan(t, "2", infinity, exitChanges))
	s.requireSavedPlanClean(t, "2", infinity)
	s.requireNoSecrets(t)
}
