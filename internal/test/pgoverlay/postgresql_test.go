//go:build pgoverlay

package pgoverlay_test

import (
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// providerDefaultVersion is the provider default for expected_version. The
// provider detects the server version when it receives this value.
const providerDefaultVersion = "9.0.0"

func postgresqlScenario(t *testing.T) *scenario {
	t.Helper()
	return newScenario(t, parseTarget(t, testenv.PostgreSQL(t), providerDefaultVersion))
}

// TestPostgreSQLPasswordUpdate changes the password version of an existing role
// and requires a working new password and an empty repeat plan.
func TestPostgreSQLPasswordUpdate(t *testing.T) {
	s := postgresqlScenario(t)
	s.firstPassword = randomText(t, `first-\quote'-`)
	s.secondPassword = randomText(t, `second-\quote'-`)
	s.requireApplied(t, "1", infinity)
	s.requireLogin(t, "1", true)
	s.requireApplied(t, "2", infinity)
	s.requireLogin(t, "2", true)
	s.requireLogin(t, "1", false)
	s.requireState(t, "2", infinity)
	s.requirePlan(t, "2", infinity, exitSuccess)
	s.requireSavedPlanClean(t, "2", infinity)
	s.requireNoSecrets(t)
}

// TestPostgreSQLFailedUpdate fails a password update at a later statement of
// the same transaction. The prior password must keep working, state must keep
// the prior password version, and the next plan must request the update.
func TestPostgreSQLFailedUpdate(t *testing.T) {
	s := postgresqlScenario(t)
	s.requireApplied(t, "1", infinity)
	output, code := s.apply(t, "2", invalidValidDate)
	if code == exitSuccess {
		t.Fatalf("apply succeeded with an invalid valid_until value:\n%s", output)
	}
	s.requireLogin(t, "1", true)
	s.requireLogin(t, "2", false)
	s.requireState(t, "1", infinity)
	s.requirePlanRequestsPassword(t, s.requirePlan(t, "2", invalidValidDate, exitChanges))
	s.requireApplied(t, "2", infinity)
	s.requireLogin(t, "2", true)
	s.requirePlan(t, "2", infinity, exitSuccess)
	s.requireSavedPlanClean(t, "2", infinity)
	s.requireNoSecrets(t)
}

// TestPostgreSQLFailedRename renames a role and fails the same update at a
// later statement. The rename must roll back, the original role must keep
// working, and state must keep the original role as its name and id.
func TestPostgreSQLFailedRename(t *testing.T) {
	s := postgresqlScenario(t)
	renamed := randomText(t, "renamed_")
	t.Cleanup(func() { s.target.dropRole(t, renamed) })
	s.requireApplied(t, "1", infinity)
	output, code := s.applyRole(t, renamed, "2", invalidValidDate)
	if code == exitSuccess {
		t.Fatalf("apply succeeded with an invalid valid_until value:\n%s", output)
	}
	if !s.target.roleExists(t, s.role) {
		t.Fatalf("the original role does not exist after the failed rename")
	}
	if s.target.roleExists(t, renamed) {
		t.Fatalf("the renamed role exists after the failed rename")
	}
	s.requireLogin(t, "1", true)
	s.requireState(t, "1", infinity)
	s.requireNoSecrets(t)
}
